package moderation

import "testing"

func TestKeywordPolicyBlocksInputTerm(t *testing.T) {
	moderator := New(true, []string{"违禁词"}, nil)
	decision := moderator.Check(DirectionInput, "这句话包含违禁词。")
	if !decision.Blocked || decision.Term != "违禁词" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestKeywordPolicySeparatesDirections(t *testing.T) {
	moderator := New(true, []string{"输入词"}, []string{"输出词"})
	if decision := moderator.Check(DirectionInput, "只有输出词"); decision.Blocked {
		t.Fatalf("input should not match output terms, decision = %#v", decision)
	}
	if decision := moderator.Check(DirectionOutput, "只有输入词"); decision.Blocked {
		t.Fatalf("output should not match input terms, decision = %#v", decision)
	}
	if decision := moderator.Check(DirectionOutput, "包含输出词的回答"); !decision.Blocked {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestKeywordPolicyIsCaseInsensitive(t *testing.T) {
	moderator := New(true, []string{"Blocked"}, nil)
	if decision := moderator.Check(DirectionInput, "contains blocked content"); !decision.Blocked {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestNewReturnsNoopWhenDisabledOrEmpty(t *testing.T) {
	for _, moderator := range []Moderator{
		New(false, []string{"违禁词"}, nil),
		New(true, nil, nil),
		New(true, []string{"   "}, []string{""}),
	} {
		if decision := moderator.Check(DirectionInput, "违禁词"); decision.Blocked {
			t.Fatalf("moderator %#v should pass everything, decision = %#v", moderator, decision)
		}
	}
}
