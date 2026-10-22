// Package bepusdt embeds the GPL-3.0 BEpusdt collection engine in Sub2API.
// See THIRD_PARTY_NOTICES.md for the pinned upstream and distribution terms.
package bepusdt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofrs/flock"
	"github.com/shopspring/decimal"
	beconf "github.com/v03413/bepusdt/app/conf"
	belog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/task"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var runtime struct {
	sync.Mutex
	ready  bool
	cancel context.CancelFunc
	lock   *flock.Flock
}

var dataRoot atomic.Value
var probeOnCreate atomic.Bool
var errEngineInUse = errors.New("built-in BEpusdt data is already in use by another Sub2API process")

func SetDataDir(dir string) { dataRoot.Store(dir) }

func DataDir() string {
	dir := os.Getenv("DATA_DIR")
	if configured, ok := dataRoot.Load().(string); ok {
		dir = configured
	}
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "bepusdt")
}

// Resume restarts persisted scanning after a server restart. Fresh deployments
// remain idle until an administrator opens the built-in payment configuration.
func Resume() error {
	if _, err := os.Stat(filepath.Join(DataDir(), "sqlite.db")); os.IsNotExist(err) {
		return nil
	}
	// A standby can serve other APIs while the active process owns collection.
	runtime.Lock()
	defer runtime.Unlock()
	if runtime.ready {
		return nil
	}
	return initialize(DataDir(), true)
}

func Ensure() error {
	runtime.Lock()
	defer runtime.Unlock()
	if runtime.ready {
		return nil
	}
	// During a rolling handoff, incoming payment requests wait briefly for the
	// previous process to finish its callbacks and release the exclusive lock.
	return waitForEngine(context.Background(), 20*time.Second, func() error { return initialize(DataDir(), true) })
}

func waitForEngine(ctx context.Context, timeout time.Duration, initialize func() error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := initialize()
		if !errors.Is(err, errEngineInUse) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

func initialize(dir string, scan bool) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, "engine.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return errEngineInUse
	}
	defer func() {
		if !runtime.ready {
			_ = lock.Unlock()
		}
	}()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "sqlite.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(8000)&_pragma=synchronous(NORMAL)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(5)
	defer func() {
		if !runtime.ready {
			_ = sqlDB.Close()
		}
	}()
	model.Db = db
	if err := model.AutoMigrate(); err != nil {
		return err
	}
	// Initialize only the collection engine. Upstream's administrator account,
	// login routes and installation page are never created or exposed.
	model.FillDefaultConf()
	if err := model.MigratePublicRPCDefaults(); err != nil {
		return err
	}
	// EVM payments must pass the network's confirmation depth, rather than
	// accepting a successful receipt as soon as it appears in a block.
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "k"}}, DoUpdates: clause.AssignmentColumns([]string{"v"})}).
		Create(&model.Conf{K: model.BlockOffsetConfirm, V: "1"}).Error; err != nil {
		return err
	}
	if model.GetK(model.ApiAuthToken) == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		if err := db.Create(&model.Conf{K: model.ApiAuthToken, V: hex.EncodeToString(secret)}).Error; err != nil {
			return err
		}
	}
	model.RefreshC()
	if err := belog.Init(filepath.Join(dir, "logs")); err != nil {
		return err
	}
	if scan {
		if err := task.Init(); err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(context.Background())
		runtime.cancel = cancel
		task.Start(ctx)
	}
	runtime.lock = lock
	probeOnCreate.Store(scan)
	runtime.ready = true
	return nil
}

// Stop cancels the scanner on process shutdown. Its DB and process lock stay
// open until exit because upstream network calls may still be finishing.
func Stop() {
	runtime.Lock()
	defer runtime.Unlock()
	if runtime.cancel != nil {
		runtime.cancel()
	}
}

func Token() (string, error) {
	if err := Ensure(); err != nil {
		return "", err
	}
	return model.AuthToken(), nil
}

type CreateRequest struct {
	OrderID, Amount, TradeType, Currency, Subject, NotifyURL, ReturnURL string
	ExpiresAt                                                           time.Time
}

func Create(ctx context.Context, req CreateRequest) (model.Order, error) {
	if err := Ensure(); err != nil {
		return model.Order{}, err
	}
	if probeOnCreate.Load() {
		config, ok := model.GetAllTradeConfig()[req.TradeType]
		if !ok {
			return model.Order{}, fmt.Errorf("unsupported payment network")
		}
		health, err := checkNetwork(ctx, string(config.Network))
		if err != nil {
			return model.Order{}, err
		}
		if health.Status != "healthy" {
			return model.Order{}, fmt.Errorf("payment network is temporarily unavailable; please try again later")
		}
	}
	walletMu.Lock()
	defer walletMu.Unlock()
	if err := ctx.Err(); err != nil {
		return model.Order{}, err
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || !amount.IsPositive() {
		return model.Order{}, fmt.Errorf("invalid payment amount")
	}
	timeout := int64(time.Until(req.ExpiresAt).Seconds())
	if timeout < 120 {
		timeout = 120
	}
	return model.StartBuildOrder(model.OrderParams{Money: amount, OrderId: req.OrderID,
		TradeType: model.TradeType(req.TradeType), Fiat: model.Fiat(req.Currency),
		ApiType: model.OrderApiTypeEpusdt, Name: req.Subject, NotifyUrl: req.NotifyURL,
		RedirectUrl: req.ReturnURL, Timeout: timeout})
}

func Order(ctx context.Context, tradeID string) (model.Order, error) {
	if err := Ensure(); err != nil {
		return model.Order{}, err
	}
	var order model.Order
	err := model.Db.WithContext(ctx).Where("trade_id = ?", tradeID).First(&order).Error
	return order, err
}

func Cancel(ctx context.Context, tradeID string) error {
	order, err := Order(ctx, tradeID)
	if err != nil {
		return err
	}
	result := model.Db.WithContext(ctx).Model(&model.Order{}).
		Where("id = ? AND status = ?", order.ID, model.OrderStatusWaiting).
		Update("status", model.OrderStatusCanceled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("payment is no longer waiting")
	}
	return nil
}

func Wallets(ctx context.Context) ([]model.Wallet, error) {
	if err := Ensure(); err != nil {
		return nil, err
	}
	wallets := make([]model.Wallet, 0)
	err := model.Db.WithContext(ctx).Order("id DESC").Find(&wallets).Error
	return wallets, err
}

// Wallet writes serialize with other administrator changes; pending orders
// protect the address and network that the scanner uses to match transfers.
var walletMu sync.Mutex

func SaveWallet(ctx context.Context, wallet model.Wallet) error {
	if err := Ensure(); err != nil {
		return err
	}
	walletMu.Lock()
	defer walletMu.Unlock()
	if !model.IsSupportedTradeType(model.TradeType(wallet.TradeType)) || (wallet.Status != 0 && wallet.Status != 1) {
		return fmt.Errorf("invalid wallet network or status")
	}
	wallet.Name, wallet.Address = strings.TrimSpace(wallet.Name), strings.TrimSpace(wallet.Address)
	if wallet.Address == "" || len(wallet.Name) > 32 || len(wallet.Remark) > 255 {
		return fmt.Errorf("invalid wallet fields")
	}
	if err := wallet.Validate(); err != nil {
		return err
	}
	return model.Db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if wallet.ID != 0 {
			var old model.Wallet
			if err := db.First(&old, wallet.ID).Error; err != nil {
				return err
			}
			if old.MatchAddr != wallet.MatchAddr || old.TradeType != wallet.TradeType || wallet.Status == 0 {
				if err := protectWallet(db, old); err != nil {
					return err
				}
			}
			return db.Model(&old).Select("name", "remark", "address", "match_addr", "trade_type", "status").Updates(&wallet).Error
		}
		return db.Create(&wallet).Error
	})
}

func protectWallet(db *gorm.DB, wallet model.Wallet) error {
	var count int64
	if err := db.Model(&model.Order{}).Where("match_address = ? AND trade_type = ? AND status IN ?", wallet.MatchAddr, wallet.TradeType,
		[]int{model.OrderStatusWaiting, model.OrderStatusConfirming}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("wallet has pending payments")
	}
	return nil
}

func DeleteWallet(ctx context.Context, id int64) error {
	if err := Ensure(); err != nil {
		return err
	}
	walletMu.Lock()
	defer walletMu.Unlock()
	return model.Db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var wallet model.Wallet
		if err := db.First(&wallet, id).Error; err != nil {
			return err
		}
		if err := protectWallet(db, wallet); err != nil {
			return err
		}
		return db.Delete(&wallet).Error
	})
}

// Only collection settings are writable. Authentication, callback targets,
// manual paid status and arbitrary upstream configuration are not exposed.
var SettingKeys = []model.ConfKey{
	model.RpcEndpointTron, model.RpcEndpointTronGridApiKey, model.RpcEndpointEthereum,
	model.RpcEndpointBsc, model.RpcEndpointPolygon, model.RpcEndpointSolana,
	model.RpcEndpointAptos, model.RpcEndpointArbitrum, model.RpcEndpointXlayer,
	model.RpcEndpointBase, model.RpcEndpointPlasma, model.RpcGlobalConfigUrlTon,
	model.RateSyncCoingeckoApiUrl, model.RateSyncCoingeckoApiKey, model.RateSyncInterval,
}

func Settings() (map[string]string, error) {
	if err := Ensure(); err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, key := range SettingKeys {
		values[string(key)] = model.GetK(key)
	}
	return values, nil
}

func SaveSettings(ctx context.Context, values map[string]string) error {
	if err := Ensure(); err != nil {
		return err
	}
	allowed := make(map[string]bool)
	for _, key := range SettingKeys {
		allowed[string(key)] = true
	}
	for key, value := range values {
		value = strings.TrimSpace(value)
		if !allowed[key] || len(value) > 512 {
			return fmt.Errorf("unsupported collection setting: %s", key)
		}
		if key == string(model.RateSyncInterval) {
			n, err := strconv.Atoi(value)
			if err != nil || n < 60 || n > 86400 {
				return fmt.Errorf("exchange rate sync interval must be 60–86400 seconds")
			}
		}
		if key == string(model.RpcEndpointTron) {
			host, port, err := net.SplitHostPort(value)
			n, portErr := strconv.Atoi(port)
			if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 || strings.ContainsAny(host, "/ \t\r\n") {
				return fmt.Errorf("Tron gRPC endpoint must be host:port")
			}
		} else if (strings.HasPrefix(key, "rpc_") && !strings.HasSuffix(key, "api_key")) || key == string(model.RateSyncCoingeckoApiUrl) {
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("%s must be an HTTP(S) URL", key)
			}
		}
	}
	err := model.Db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		for key, value := range values {
			row := model.Conf{K: model.ConfKey(key), V: strings.TrimSpace(value)}
			if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "k"}}, DoUpdates: clause.AssignmentColumns([]string{"v"})}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		model.RefreshC()
		invalidateNodeChecks()
	}
	return err
}

func Rates(ctx context.Context) ([]model.Rate, error) {
	if err := Ensure(); err != nil {
		return nil, err
	}
	rates := make([]model.Rate, 0)
	err := model.Db.WithContext(ctx).Order("id DESC").Limit(50).Find(&rates).Error
	return rates, err
}

func SyncRates() error {
	if err := Ensure(); err != nil {
		return err
	}
	return model.CoingeckoRate()
}

func SaveRate(ctx context.Context, fiat, crypto, value string) error {
	if err := Ensure(); err != nil {
		return err
	}
	if _, ok := model.GetSupportFiat()[model.Fiat(fiat)]; !ok {
		return fmt.Errorf("unsupported fiat")
	}
	if _, ok := model.GetSupportCrypto()[model.Crypto(crypto)]; !ok {
		return fmt.Errorf("unsupported cryptocurrency")
	}
	rate, err := decimal.NewFromString(value)
	if err != nil || !rate.IsPositive() || rate.GreaterThan(decimal.NewFromInt(1000000000)) {
		return fmt.Errorf("invalid exchange rate")
	}
	f, _ := rate.Float64()
	return model.Db.WithContext(ctx).Create(&model.Rate{Fiat: fiat, Crypto: crypto, Rate: rate.String(), RawRate: f}).Error
}

func Stats() any { return beconf.GetStats() }

func IsNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
