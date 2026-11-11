package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

const PelicanQuestionBankVersion = "svg-motion-v1"
const PelicanPromptModeRandom = "random"

// PelicanQuestion is immutable evidence copied into each result, including the
// visual review rubric. Transport success is not a judgment of drawing quality.
type PelicanQuestion struct {
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	RoundID  string   `json:"round_id"`
	Source   string   `json:"source"`
	Prompt   string   `json:"prompt"`
	Criteria []string `json:"criteria"`
}

type pelicanQuestionTask struct {
	id       string
	task     string
	criteria []string
}

// Each task has two wheels, one articulated character, coordinated cyclic
// motion and a moving background. Review each task against its own rubric.
var pelicanQuestionBank = []pelicanQuestionTask{
	{"pelican-bicycle", "鹈鹕骑自行车：画出长喙和喉囊、双轮车架、脚踏与腿部联动，背景路标向后移动。", []string{"长喙与喉囊清晰可辨", "两轮、车架及脚踏结构完整", "车轮转动与双腿踩踏连续协调", "路标向后移动，角色保持在画面内"}},
	{"penguin-bicycle", "企鹅骑自行车：画出黑白身体和短喙、双轮车架、脚踏与双脚联动，背景冰块向后移动。", []string{"黑白身体与短喙清晰可辨", "两轮、车架及脚踏结构完整", "车轮转动与双脚踩踏连续协调", "冰块向后移动，角色保持在画面内"}},
	{"rabbit-scooter", "兔子骑双轮滑板车：画出长耳、踏板和车把，一脚站立、另一脚蹬地后收回，背景草丛向后移动。", []string{"长耳与兔子轮廓清晰可辨", "两轮、踏板及车把结构完整", "车轮转动与单腿蹬地收回连续协调", "草丛向后移动，角色保持在画面内"}},
	{"otter-scooter", "水獭骑双轮滑板车：画出圆耳和长尾、踏板和车把，一脚站立、另一脚蹬地后收回，背景石块向后移动。", []string{"圆耳与长尾清晰可辨", "两轮、踏板及车把结构完整", "车轮转动与单腿蹬地收回连续协调", "石块向后移动，角色保持在画面内"}},
}

// A scheduled round is the nominal due time, not the worker start time or
// account ID. Replicas and delayed workers therefore select the same task.
func pelicanQuestionIndex(roundID string, size int) int {
	digest := sha256.Sum256([]byte(PelicanQuestionBankVersion + ":" + roundID))
	return int(binary.BigEndian.Uint64(digest[:8]) % uint64(size))
}

// selectIndex is injectable for deterministic multi-round tests.
func freezePelicanQuestion(cfg *PelicanTestConfig, roundID string, selectIndex func(int) int) *PelicanTestConfig {
	if cfg == nil {
		return nil
	}
	snapshot := *cfg
	snapshot.PelicanQuestion = nil
	if cfg.PromptMode == PelicanPromptModeRandom {
		index := pelicanQuestionIndex(roundID, len(pelicanQuestionBank))
		if selectIndex != nil {
			index = selectIndex(len(pelicanQuestionBank))
		}
		task := pelicanQuestionBank[index]
		snapshot.Prompt = "创建一个独立 HTML，使用内联 SVG 绘制循环的 2D 动画。" + task.task + "不得使用外部资源。"
		snapshot.PelicanQuestion = &PelicanQuestion{
			ID: task.id, Version: PelicanQuestionBankVersion, RoundID: roundID,
			Source: "builtin:svg-motion", Prompt: snapshot.Prompt,
			Criteria: append([]string(nil), task.criteria...),
		}
	}
	snapshot.ActualPrompt = intelligenceTestPrompt(&snapshot)
	return &snapshot
}

// NewPelicanQuestion prepares a manual round without making an upstream call.
// Clients reuse this snapshot for every account/sample in the comparison.
func NewPelicanQuestion() (*PelicanTestConfig, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return freezePelicanQuestion(&PelicanTestConfig{QuestionKind: "pelican", PromptMode: PelicanPromptModeRandom}, "manual:"+hex.EncodeToString(nonce[:]), nil), nil
}

// ResolveManualPelicanQuestion validates the server-issued identity and rebuilds
// its rubric; client-supplied prompt/rubric cannot relabel a built-in question.
func ResolveManualPelicanQuestion(question *PelicanQuestion) (*PelicanTestConfig, error) {
	if question == nil || !strings.HasPrefix(question.RoundID, "manual:") || len(question.RoundID) != len("manual:")+32 {
		return nil, fmt.Errorf("invalid pelican question round")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(question.RoundID, "manual:")); err != nil {
		return nil, fmt.Errorf("invalid pelican question round")
	}
	cfg := freezePelicanQuestion(&PelicanTestConfig{QuestionKind: "pelican", PromptMode: PelicanPromptModeRandom}, question.RoundID, nil)
	if question.ID != cfg.PelicanQuestion.ID || question.Version != cfg.PelicanQuestion.Version {
		return nil, fmt.Errorf("pelican question version or identity changed; start a new round")
	}
	return cfg, nil
}

// Only SVG tests can opt into this bank; missing mode keeps legacy plans intact.
func validatePelicanPromptMode(cfg *PelicanTestConfig) error {
	switch cfg.PromptMode {
	case "", "custom":
		return nil
	case PelicanPromptModeRandom:
		if (cfg.QuestionKind != "" && cfg.QuestionKind != "pelican") || cfg.Quality != nil {
			return fmt.Errorf("random SVG questions require a pelican test without automatic grading")
		}
		return nil
	default:
		return fmt.Errorf("invalid pelican prompt mode")
	}
}
