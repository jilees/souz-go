package selection

import (
	"context"
	"errors"
	"testing"

	"souz.ru/souz-go/pkg/providers"
)

type fakeProvider struct {
	resp   *providers.ChatResponse
	err    error
	gotReq providers.ChatRequest
}

func (f *fakeProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}
func (f *fakeProvider) ChatStream(_ context.Context, req providers.ChatRequest, _ func(string)) (*providers.ChatResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func candidates() []Candidate {
	return []Candidate{
		{SkillID: "weather-lookup", Name: "Weather Lookup", Description: "Looks up weather"},
		{SkillID: "pdf-summarize", Name: "PDF Summarize", Description: "Summarizes PDFs"},
	}
}

func TestSelect_ReturnsKnownSelectedIDs(t *testing.T) {
	provider := &fakeProvider{resp: &providers.ChatResponse{
		Content: `{"selectedSkillIds":["weather-lookup"],"rationale":"user asked about weather"}`,
	}}

	result, err := Select(context.Background(), provider, "what's the weather in Tallinn?", candidates())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(result.SelectedSkillIDs) != 1 || result.SelectedSkillIDs[0] != "weather-lookup" {
		t.Errorf("unexpected selection: %+v", result)
	}
	if result.Rationale == "" {
		t.Error("expected a rationale")
	}
}

func TestSelect_DropsUnknownIDs(t *testing.T) {
	provider := &fakeProvider{resp: &providers.ChatResponse{
		Content: `{"selectedSkillIds":["weather-lookup","made-up-skill"],"rationale":"..."}`,
	}}

	result, err := Select(context.Background(), provider, "weather?", candidates())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(result.SelectedSkillIDs) != 1 || result.SelectedSkillIDs[0] != "weather-lookup" {
		t.Errorf("expected unknown id dropped, got %+v", result.SelectedSkillIDs)
	}
}

func TestSelect_NoCandidatesShortCircuits(t *testing.T) {
	provider := &fakeProvider{err: errors.New("should not be called")}
	result, err := Select(context.Background(), provider, "hi", nil)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(result.SelectedSkillIDs) != 0 {
		t.Errorf("expected empty selection, got %+v", result)
	}
}

func TestSelect_UnparseableResponseFailsClosedToEmpty(t *testing.T) {
	provider := &fakeProvider{resp: &providers.ChatResponse{Content: "sure, sounds good"}}
	result, err := Select(context.Background(), provider, "hi", candidates())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(result.SelectedSkillIDs) != 0 {
		t.Errorf("expected empty selection on unparseable output, got %+v", result)
	}
}

func TestSelect_ProviderErrorIsReturned(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	if _, err := Select(context.Background(), provider, "hi", candidates()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSelect_EmptySelectionIsValid(t *testing.T) {
	provider := &fakeProvider{resp: &providers.ChatResponse{
		Content: `{"selectedSkillIds":[],"rationale":"just chit-chat, no skill needed"}`,
	}}
	result, err := Select(context.Background(), provider, "how are you?", candidates())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(result.SelectedSkillIDs) != 0 {
		t.Errorf("expected empty selection, got %+v", result.SelectedSkillIDs)
	}
}
