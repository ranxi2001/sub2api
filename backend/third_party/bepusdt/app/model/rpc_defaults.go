package model

import "strings"

// Replace only retired bundled defaults. Custom URLs, API keys, wallets and
// order settings stay under the administrator's control.
func MigratePublicRPCDefaults() error {
	oldDefaults := map[ConfKey]string{
		RpcEndpointBsc:      "https://binance-smart-chain-public.nodies.app",
		RpcEndpointEthereum: "https://ethereum-public.nodies.app",
		RpcEndpointPolygon:  "https://polygon-public.nodies.app",
		RpcEndpointBase:     "https://base-public.nodies.app",
	}
	for key, old := range oldDefaults {
		value := GetK(key)
		if strings.TrimRight(value, "/") != old {
			continue
		}
		if err := Db.Model(&Conf{}).Where("k = ? AND v = ?", key, value).Update("v", defaultConf[key]).Error; err != nil {
			return err
		}
	}
	return nil
}
