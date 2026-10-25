package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openai/openai-go/v3"

	"horkilion/internal/agent/plan"
)

// fakeModel is an OpenAI-compatible endpoint that answers with the scripted
// replies in order and records every request.
type fakeModel struct {
	mu       sync.Mutex
	replies  []func(req map[string]any) map[string]any
	requests []map[string]any
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	n := len(f.requests)
	f.mu.Unlock()
	if req["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"s","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"итог без инструментов"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	msg := f.replies[min(n, len(f.replies))-1](req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "c", "object": "chat.completion", "created": 0, "model": "m",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

func toolCall(id, name string, args any) map[string]any {
	raw, _ := json.Marshal(args)
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}
}

func withModel(t *testing.T, f *fakeModel) *Agent {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := inventoryAgent(t)
	a.llm = NewLLM(LLMConfig{APIKey: "test", BaseURL: srv.URL, Model: "m", RouterModel: "r"})
	return a
}

func assembledPlan(t *testing.T, a *Agent) plan.TripPlan {
	t.Helper()
	p := plan.New("s")
	turn(t, a, &p, plan.Patch{Destination: ptr("Сочи"), Dates: &plan.DatesPatch{Depart: ptr("2026-11-20")}})
	p.NextStage()
	return p
}

func TestToolLoopSearchesAndExplains(t *testing.T) {
	f := &fakeModel{}
	a := withModel(t, f)
	p := assembledPlan(t, a)
	hotel := p.Components.Stay.Options[1].ID
	f.replies = []func(map[string]any) map[string]any{
		func(req map[string]any) map[string]any {
			return map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				toolCall("c1", "search_hotels", map[string]any{"meal": "breakfast"}),
				toolCall("c2", "explain_options", map[string]any{"component": "stay", "items": []any{
					map[string]any{"option_id": hotel, "why": "рядом с морем", "tradeoffs": []string{"без бассейна"}, "risks": []string{}},
				}}),
				toolCall("c3", "book_everything", map[string]any{}),
			}}
		},
		func(req map[string]any) map[string]any {
			return map[string]any{"role": "assistant", "content": "Вот три отеля."}
		},
	}
	var tokens []string
	text, err := a.toolLoop(context.Background(), &p, []openai.ChatCompletionMessageParamUnion{openai.UserMessage("подбери отель с завтраком")},
		func(s string) { tokens = append(tokens, s) })
	if err != nil || text != "Вот три отеля." || strings.Join(tokens, "") != text {
		t.Fatalf("want the final text streamed, got %q %v %v", text, tokens, err)
	}

	// The model saw only the tools of its stage.
	var names []string
	for _, tl := range f.requests[0]["tools"].([]any) {
		names = append(names, tl.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "search_hotels,get_visa_requirement,explain_options" {
		t.Errorf("assemble must offer only its tools, got %v", names)
	}
	// Every call got a result back, an unknown tool an error.
	results := map[string]string{}
	for _, m := range f.requests[1]["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "tool" {
			results[mm["tool_call_id"].(string)] = fmt.Sprint(mm["content"])
		}
	}
	if !strings.Contains(results["c1"], `"hotels"`) || !strings.Contains(results["c2"], `"saved":1`) || !strings.Contains(results["c3"], "unknown tool") {
		t.Errorf("tool results are wrong: %v", results)
	}
	if e := p.Components.Stay.Options[1].Explanation; e == nil || e.Why != "рядом с морем" {
		t.Errorf("explain_options must write the explanation into the plan, got %+v", e)
	}
	if p.Components.Stay.SelectedID != "" {
		t.Error("tools must not select anything")
	}
}

func TestToolLoopStopsAfterMaxSteps(t *testing.T) {
	f := &fakeModel{}
	a := withModel(t, f)
	p := assembledPlan(t, a)
	f.replies = []func(map[string]any) map[string]any{func(map[string]any) map[string]any {
		return map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{toolCall("c", "get_visa_requirement", map[string]any{"country_code": "TR"})}}
	}}
	text, err := a.toolLoop(context.Background(), &p, []openai.ChatCompletionMessageParamUnion{openai.UserMessage("а виза в Турцию?")}, func(string) {})
	if err != nil || text != "итог без инструментов" || len(f.requests) != maxToolSteps+1 {
		t.Errorf("want %d tool steps and a final answer without tools, got %d requests, %q, %v", maxToolSteps, len(f.requests), text, err)
	}
	if _, ok := f.requests[maxToolSteps]["tools"]; ok {
		t.Error("the final request must not offer tools")
	}
}

func TestStaysAreExplainedWithoutLLM(t *testing.T) {
	a := inventoryAgent(t)
	p := plan.New("s")
	turn(t, a, &p, plan.Patch{Destination: ptr("Сочи"), Dates: &plan.DatesPatch{Depart: ptr("2026-11-20")}})
	p.ExplainStays()
	for _, o := range p.Components.Stay.Options[:3] {
		if o.Explanation == nil || o.Explanation.Why == "" {
			t.Errorf("%s: the first three hotels need an explanation", o.ID)
		}
	}
}
