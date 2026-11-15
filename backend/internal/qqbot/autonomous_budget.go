package qqbot

import (
	"encoding/json"
	"errors"
	"strings"
)

// Text uses the existing local tokenizer. Image accounting is a planning
// allowance, not provider billing; actual input/output usage remains separate.
const autonomousImageAllowance = 1024

type AutonomousInputEstimate struct {
	System    int `json:"system"`
	Knowledge int `json:"knowledge"`
	History   int `json:"history"`
	Current   int `json:"current"`
	Images    int `json:"images"`
	Total     int `json:"total"`
	Removed   int `json:"removed"`
}

type AutonomousExecution struct {
	Mode             string                  `json:"mode"`
	BatchMessages    int                     `json:"batch_messages"`
	MergedHistoryIDs []string                `json:"merged_history_ids,omitempty"`
	InputEstimate    AutonomousInputEstimate `json:"input_estimate"`
	ModelStarted     bool                    `json:"model_started"`
	ModelMS          int64                   `json:"model_ms"`
}

func estimateAutonomousInput(messages []Message) AutonomousInputEstimate {
	var out AutonomousInputEstimate
	for i, m := range messages {
		n := groupHistoryTokens(m.Content) + 8
		switch {
		case i == len(messages)-1:
			out.Current += n
		case strings.HasPrefix(m.Content, siteKnowledgeInstructions):
			out.Knowledge += n
		case m.Role == "system" || m.Role == "developer":
			out.System += n
		default:
			out.History += n
		}
		out.Images += len(m.Images) * autonomousImageAllowance
	}
	out.Total = out.System + out.Knowledge + out.History + out.Current + out.Images
	return out
}

// Preserve the current message, persona and protocol. Drop oldest history, then
// entire optional knowledge entries; never truncate a JSON data wrapper.
func budgetAutonomousInput(messages []Message, budget int, mergedIDs []string) ([]Message, AutonomousInputEstimate, error) {
	out := make([]Message, 0, len(messages))
	seenSystem := map[string]bool{}
	for i, m := range messages {
		if i < len(messages)-1 {
			if m.Role == "system" || m.Role == "developer" {
				if strings.TrimSpace(m.Content) == "" || seenSystem[m.Content] {
					continue
				}
				seenSystem[m.Content] = true
			}
			if m.Role == "user" {
				var entry groupHistoryEntry
				if json.Unmarshal([]byte(m.Content), &entry) == nil {
					merged := false
					for _, id := range mergedIDs {
						if entry.ID == id {
							merged = true
							break
						}
					}
					if merged {
						continue
					}
				}
			}
		}
		out = append(out, m)
	}
	original := estimateAutonomousInput(messages).Total
	for estimateAutonomousInput(out).Total > budget {
		index := -1
		for i := 0; i < len(out)-1; i++ {
			if out[i].Role != "system" && out[i].Role != "developer" {
				index = i
				break
			}
		}
		if index < 0 {
			for i := 0; i < len(out)-1; i++ {
				if strings.HasPrefix(out[i].Content, siteKnowledgeInstructions) {
					index = i
					break
				}
			}
		}
		if index < 0 {
			estimate := estimateAutonomousInput(out)
			estimate.Removed = original - estimate.Total
			return out, estimate, errors.New("当前消息、图片及必需提示词超过自动接话输入预算，请提高预算或缩短人设")
		}
		out = append(out[:index], out[index+1:]...)
	}
	estimate := estimateAutonomousInput(out)
	estimate.Removed = original - estimate.Total
	return out, estimate, nil
}
