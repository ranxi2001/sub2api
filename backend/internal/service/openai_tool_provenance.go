package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Only hashes and delivery/route metadata; never tool arguments/results. A
// separate bounded namespace prevents client history from minting call grants.
const OpenAIToolProvenanceMaxBytes = 32 * 1024

type OpenAIToolProvenanceCache interface {
	GetOpenAIToolProvenance(context.Context, string) (string, error)
	ReserveOpenAIToolProvenance(context.Context, string, string, time.Duration) error
	FinishOpenAIToolProvenance(context.Context, string, string, string, bool, time.Duration) error
}

type openAIToolAnchor struct {
	Owner    string `json:"owner"`
	EpochKey string `json:"epoch_key,omitempty"`
	Epoch    string `json:"epoch,omitempty"`
}

type openAIToolProof struct {
	Version         int                         `json:"version"`
	Call            string                      `json:"call"`
	Producer        string                      `json:"producer"`
	Delivery        string                      `json:"delivery"`
	Anchors         map[string]openAIToolAnchor `json:"anchors"`
	ShadowPrincipal string                      `json:"shadow_principal,omitempty"`
	ShadowTask      string                      `json:"shadow_task,omitempty"`
	ShadowDigest    string                      `json:"shadow_digest,omitempty"`
	ShadowEpoch     string                      `json:"shadow_epoch,omitempty"`
}

type openAIToolObservation struct {
	fingerprint string
	complete    bool
}

type openAIToolProofSnapshot struct {
	raw   string
	proof openAIToolProof
}

type openAIToolLocalRecord struct {
	raw   string
	until time.Time
}

func openAIToolHash(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (r *openAIAgentRecoveryRequest) toolKey(id string) string {
	// The forward model is the authoritative current-turn scope. Raw WS
	// frames may omit model after a successful selection; shadowModel is an
	// optional input field for shadow lookup, not a second tool-proof scope.
	return r.shadowPrincipal() + ":" + openAIToolHash([]any{"tool-v1", r.userID, r.keyID, r.groupID, r.session, r.model, id})
}

func openAIToolFingerprint(item gjson.Result) (string, bool) {
	kind := item.Get("type").String()
	if kind != "function_call" && kind != "custom_tool_call" {
		return "", false
	}
	for _, field := range []string{"call_id", "name"} {
		if item.Get(field).Type != gjson.String || strings.TrimSpace(item.Get(field).String()) == "" {
			return "", false
		}
	}
	if item.Get("status").Exists() && item.Get("status").String() != "completed" {
		return "", false
	}
	field := "arguments"
	if kind == "custom_tool_call" {
		field = "input"
	}
	if item.Get(field).Type != gjson.String {
		return "", false
	}
	// Preserve exact argument/input bytes and every semantic extension. Only
	// provider item IDs/status are replay metadata; call_id is always bound.
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(item.Raw), &fields) != nil {
		return "", false
	}
	delete(fields, "id")
	delete(fields, "status")
	return openAIToolHash(fields), true
}

func (s *OpenAIGatewayService) toolProofGet(ctx context.Context, key string) (string, error) {
	if cache, ok := s.cache.(OpenAIToolProvenanceCache); ok {
		return cache.GetOpenAIToolProvenance(ctx, key)
	}
	if s.cache != nil {
		return "", openAIAgentRecoveryError("store_unavailable")
	}
	s.agentMessageRecovery.mu.Lock()
	defer s.agentMessageRecovery.mu.Unlock()
	r := s.agentMessageRecovery.toolProofs[key]
	if time.Now().After(r.until) {
		return "", nil
	}
	return r.raw, nil
}

func (s *OpenAIGatewayService) toolProofReserve(ctx context.Context, key, token string) error {
	if cache, ok := s.cache.(OpenAIToolProvenanceCache); ok {
		return cache.ReserveOpenAIToolProvenance(ctx, key, token, OpenAIReadableShadowTTL)
	}
	if s.cache != nil {
		return openAIAgentRecoveryError("store_unavailable")
	}
	s.agentMessageRecovery.mu.Lock()
	defer s.agentMessageRecovery.mu.Unlock()
	state := &s.agentMessageRecovery
	if state.toolProofs == nil {
		state.toolProofs = make(map[string]openAIToolLocalRecord)
	}
	for k, v := range state.toolProofs {
		if time.Now().After(v.until) {
			delete(state.toolProofs, k)
		}
	}
	old := state.toolProofs[key]
	if len(state.toolProofs) >= 128 && old.raw == "" {
		return openAIAgentRecoveryError("store_unavailable")
	}
	// Even at capacity, reissuing an existing identity must revoke its old
	// proof. Rejecting before this update would leave the old grant usable.
	if old.raw != "" && old.raw != token {
		token = "conflict"
	}
	state.toolProofs[key] = openAIToolLocalRecord{raw: token, until: time.Now().Add(OpenAIReadableShadowTTL)}
	return nil
}

func (s *OpenAIGatewayService) toolProofFinish(ctx context.Context, key, token, record string, completed bool) {
	if cache, ok := s.cache.(OpenAIToolProvenanceCache); ok {
		_ = cache.FinishOpenAIToolProvenance(ctx, key, token, record, completed, OpenAIReadableShadowTTL)
		return
	}
	if s.cache != nil {
		return
	}
	s.agentMessageRecovery.mu.Lock()
	defer s.agentMessageRecovery.mu.Unlock()
	old := s.agentMessageRecovery.toolProofs[key]
	if old.raw != token || time.Now().After(old.until) {
		return
	}
	old.raw = "failed"
	if completed {
		old.raw = record
	}
	s.agentMessageRecovery.toolProofs[key] = old
}

// The no-shared-cache mode still needs association epochs. It never falls back
// after an error from a configured shared store.
func (s *OpenAIGatewayService) toolEpoch(ctx context.Context, key string) (string, error) {
	if epochs, ok := s.cache.(OpenAIAgentRecoveryEpochCache); ok {
		return epochs.GetOpenAIAgentRecoveryEpoch(ctx, key)
	}
	if s.cache != nil {
		return "", openAIAgentRecoveryError("store_unavailable")
	}
	s.agentMessageRecovery.mu.Lock()
	defer s.agentMessageRecovery.mu.Unlock()
	record := s.agentMessageRecovery.toolEpochs[key]
	if time.Now().After(record.until) {
		return "", nil
	}
	return record.raw, nil
}

func (s *OpenAIGatewayService) localToolEpoch(key, token string, finish, completed bool) {
	s.agentMessageRecovery.mu.Lock()
	defer s.agentMessageRecovery.mu.Unlock()
	state := &s.agentMessageRecovery
	if state.toolEpochs == nil {
		state.toolEpochs = make(map[string]openAIToolLocalRecord)
	}
	for k, v := range state.toolEpochs {
		if time.Now().After(v.until) {
			delete(state.toolEpochs, k)
		}
	}
	old := state.toolEpochs[key]
	value := token
	if finish {
		if old.raw != token {
			return
		}
		value = "failed"
		if completed {
			value = "ready:" + token
		}
	} else {
		if len(state.toolEpochs) >= 128 && old.raw == "" {
			return
		}
		if old.raw == "conflict" || (strings.HasPrefix(old.raw, "pending:") && old.raw != token) {
			value = "conflict"
		}
	}
	state.toolEpochs[key] = openAIToolLocalRecord{raw: value, until: time.Now().Add(OpenAIReadableShadowTTL)}
}

// Snapshot the original task associations before a generation is sent. Missing
// provenance loses future recovery availability, not the ordinary generation.
func (s *OpenAIGatewayService) toolInputAnchors(ctx context.Context, r *openAIAgentRecoveryRequest) (map[string]openAIToolAnchor, bool) {
	anchors := make(map[string]openAIToolAnchor)
	for source := r; source != nil; source = source.omittedSource {
		for _, key := range source.keys {
			binding, ok := source.completedOwners[key]
			if !ok {
				return nil, false
			}
			anchor := openAIToolAnchor{Owner: encodeOpenAIAgentRecoveryBinding(binding)}
			for i, k := range source.keys {
				if k == key {
					if i >= len(source.items) {
						return nil, false
					}
					anchor.EpochKey = source.shadowEpochKey(source.items[i])
					break
				}
			}
			var err error
			anchor.Epoch, err = s.toolEpoch(ctx, anchor.EpochKey)
			if err != nil || !strings.HasPrefix(anchor.Epoch, "ready:pending:") {
				return nil, false
			}
			encoded := hex.EncodeToString(key[:])
			if old, exists := anchors[encoded]; exists && old != anchor {
				return nil, false
			}
			anchors[encoded] = anchor
		}
	}
	if len(anchors) > 64 {
		return nil, false
	}
	return anchors, true
}

func (s *OpenAIGatewayService) checkToolAnchors(ctx context.Context, anchors map[string]openAIToolAnchor) error {
	if len(anchors) == 0 || len(anchors) > 64 {
		return openAIAgentRecoveryError("owner_conflict")
	}
	for key, anchor := range anchors {
		var raw string
		if store, ok := s.cache.(OpenAIAgentRecoveryCache); ok {
			var err error
			raw, err = store.GetOpenAIAgentRecovery(ctx, key)
			if err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
		} else if s.cache == nil {
			decoded, err := hex.DecodeString(key)
			if err != nil || len(decoded) != 32 {
				return openAIAgentRecoveryError("owner_conflict")
			}
			var digest [32]byte
			copy(digest[:], decoded)
			s.agentMessageRecovery.mu.Lock()
			b, found := s.agentMessageRecovery.bindings[digest]
			s.agentMessageRecovery.mu.Unlock()
			if !found || time.Now().After(b.expiresAt) || b.failed || b.conflicted || b.pending != "" {
				return openAIAgentRecoveryError("owner_conflict")
			}
			raw = encodeOpenAIAgentRecoveryBinding(b)
		} else {
			return openAIAgentRecoveryError("store_unavailable")
		}
		if raw != anchor.Owner {
			return openAIAgentRecoveryError("owner_conflict")
		}
		if anchor.EpochKey == "" {
			return openAIAgentRecoveryError("owner_conflict")
		}
		epoch, err := s.toolEpoch(ctx, anchor.EpochKey)
		if err != nil {
			return openAIAgentRecoveryError("store_unavailable")
		}
		if epoch != anchor.Epoch || !strings.HasPrefix(epoch, "ready:pending:") {
			return openAIAgentRecoveryError("owner_conflict")
		}
	}
	return nil
}

// Called on the final CLIENT representation before its physical write. A done
// item reserves identity but only a successfully delivered terminal can commit.
func (d *openAIAgentRecoveryDelivery) observeTools(body []byte, nested bool) {
	r := d.request
	if !d.toolEnabled || r.keyID <= 0 {
		return
	}
	if d.tools == nil {
		d.tools = make(map[string]openAIToolObservation)
	}
	storeCtx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	path := "output"
	if nested {
		path = "response.output"
	}
	items := gjson.GetBytes(body, path).Array()
	if nested && gjson.GetBytes(body, "item").IsObject() {
		items = append(items, gjson.GetBytes(body, "item"))
	}
	terminal := openAIAgentCompletedResponse(body, nested)
	if terminal {
		d.toolTerminal = true
		if len(items) > 0 {
			d.toolFinal = make(map[string]bool)
		}
	}
	seen := make(map[string]bool)
	for _, item := range items {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			continue
		}
		id := item.Get("call_id")
		if id.Type != gjson.String || id.String() == "" || len(d.tools) >= 64 {
			d.toolInvalid = true
			continue
		}
		key := r.toolKey(id.String())
		if seen[key] {
			d.toolInvalid = true
		}
		seen[key] = true
		if terminal && d.toolFinal != nil {
			d.toolFinal[key] = true
		}
		old, exists := d.tools[key]
		if !exists {
			if d.service.toolProofReserve(storeCtx, key, d.pending) != nil {
				d.toolInvalid = true
			}
		}
		complete := terminal || (nested && gjson.GetBytes(body, "type").String() == "response.output_item.done")
		fingerprint, valid := openAIToolFingerprint(item)
		if complete && !valid {
			d.toolInvalid = true
		}
		if complete && old.complete && old.fingerprint != fingerprint {
			d.toolInvalid = true
		}
		if complete {
			old = openAIToolObservation{fingerprint: fingerprint, complete: valid}
		}
		d.tools[key] = old
	}
}

func (d *openAIAgentRecoveryDelivery) finishTools(ctx context.Context, completed bool) {
	if len(d.tools) == 0 {
		return
	}
	anchors := make(map[string]openAIToolAnchor)
	for key, anchor := range d.toolAnchors {
		anchors[key] = anchor
	}
	for _, item := range d.request.outputs {
		key := d.request.itemKey(item)
		if !d.keys[key] {
			continue
		}
		anchor := openAIToolAnchor{Owner: encodeOpenAIAgentRecoveryBinding(d.binding)}
		anchor.EpochKey = d.request.shadowEpochKey(item)
		anchor.Epoch = "ready:" + d.pending
		anchors[hex.EncodeToString(key[:])] = anchor
	}
	valid := completed && d.toolTerminal && !d.toolInvalid && d.toolAnchorsValid && d.service.checkToolAnchors(ctx, anchors) == nil
	if d.request.shadowObservation != nil && d.service.checkOpenAIReadableShadowObservation(ctx, d.request.shadowObservation) != nil {
		valid = false
	}
	for key, call := range d.tools {
		proof := openAIToolProof{Version: 1, Call: call.fingerprint, Producer: encodeOpenAIAgentRecoveryBinding(d.binding), Delivery: openAIToolHash(d.pending), Anchors: anchors}
		if shadow := d.request.shadowObservation; shadow != nil {
			proof.ShadowPrincipal, proof.ShadowTask = shadow.principal, shadow.task
			proof.ShadowDigest, proof.ShadowEpoch = openAIToolHash(shadow.raw), shadow.epoch
		}
		raw, _ := json.Marshal(proof)
		d.service.toolProofFinish(ctx, key, d.pending, string(raw), valid && call.complete && (d.toolFinal == nil || d.toolFinal[key]) && len(raw) <= OpenAIToolProvenanceMaxBytes)
	}
}

func (s *OpenAIGatewayService) checkToolProof(ctx context.Context, proof openAIToolProof) error {
	if proof.Version != 1 || proof.Call == "" || proof.Producer == "" || proof.Delivery == "" {
		return openAIAgentRecoveryError("owner_conflict")
	}
	if err := s.checkToolAnchors(ctx, proof.Anchors); err != nil {
		return err
	}
	if proof.ShadowTask != "" {
		store, ok := s.cache.(OpenAIReadableShadowCache)
		epochs, hasEpoch := s.cache.(OpenAIAgentRecoveryEpochCache)
		if !ok || !hasEpoch {
			return openAIAgentRecoveryError("store_unavailable")
		}
		raw, err := store.GetOpenAIReadableShadow(ctx, proof.ShadowPrincipal, proof.ShadowTask)
		if err != nil {
			return openAIAgentRecoveryError("store_unavailable")
		}
		epoch, err := epochs.GetOpenAIAgentRecoveryEpoch(ctx, proof.ShadowPrincipal+":"+proof.ShadowTask)
		if err != nil {
			return openAIAgentRecoveryError("store_unavailable")
		}
		if openAIToolHash(raw) != proof.ShadowDigest || epoch != proof.ShadowEpoch {
			return openAIAgentRecoveryError("owner_conflict")
		}
	}
	return nil
}

func (s *OpenAIGatewayService) verifyOpenAIToolInputs(ctx context.Context, r *openAIAgentRecoveryRequest, body []byte) error {
	if _, reason := inspectOpenAIWSCompletedTools(body); reason != "" {
		return openAIRecoveryDiagnosticError("owner_conflict", reason, r)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	available := make(map[string]bool)
	for _, key := range r.keys {
		available[hex.EncodeToString(key[:])] = true
	}
	for key := range r.shadowFences {
		available[key] = true
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			continue
		}
		fingerprint, ok := openAIToolFingerprint(item)
		if !ok || r.userID <= 0 {
			return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_unknown", r)
		}
		key := r.toolKey(item.Get("call_id").String())
		raw, err := s.toolProofGet(ctx, key)
		for err == nil && strings.HasPrefix(raw, "pending:") {
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_unknown", r)
			case <-timer.C:
			}
			raw, err = s.toolProofGet(ctx, key)
		}
		if err != nil {
			return openAIRecoveryDiagnosticError("store_unavailable", "tool_provenance_unavailable", r)
		}
		var proof openAIToolProof
		if len(raw) > OpenAIToolProvenanceMaxBytes || json.Unmarshal([]byte(raw), &proof) != nil || proof.Call != fingerprint {
			return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_unknown", r)
		}
		for anchor := range proof.Anchors {
			if !available[anchor] {
				return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_task_changed", r)
			}
		}
		if err := s.checkToolProof(ctx, proof); err != nil {
			return withOpenAIRecoveryDiagnostics(err, r, "tool_provenance_changed")
		}
		if r.toolProofs == nil {
			r.toolProofs = make(map[string]openAIToolProofSnapshot)
		}
		r.toolProofs[key] = openAIToolProofSnapshot{raw: raw, proof: proof}
	}
	return nil
}

func (s *OpenAIGatewayService) recheckOpenAIToolProofs(ctx context.Context, r *openAIAgentRecoveryRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for key, snapshot := range r.toolProofs {
		raw, err := s.toolProofGet(ctx, key)
		if err != nil {
			return openAIRecoveryDiagnosticError("store_unavailable", "tool_provenance_unavailable", r)
		}
		if raw != snapshot.raw {
			return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_changed", r)
		}
		if err := s.checkToolProof(ctx, snapshot.proof); err != nil {
			return withOpenAIRecoveryDiagnostics(err, r, "tool_provenance_changed")
		}
	}
	return nil
}
