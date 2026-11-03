package send

import (
	"strings"
	"testing"
)

func TestTextMessage(t *testing.T) {
	m := Text("你好")
	if m.Kind() != KindText || m.Content() != "你好" {
		t.Fatalf("Text() = kind=%v content=%q", m.Kind(), m.Content())
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := Text("   ").Validate(); err == nil {
		t.Fatal("Validate(blank text) = nil, want error")
	}
	if err := (*Message)(nil).Validate(); err == nil {
		t.Fatal("Validate(nil) = nil, want error")
	}
}

func TestReplyAndReference(t *testing.T) {
	m := Text("hi").ReplyTo("msg-1", 2).Reference("ref-1")
	if id, seq := m.Reply(); id != "msg-1" || seq != 2 {
		t.Fatalf("Reply() = %q/%d, want msg-1/2", id, seq)
	}
	if m.ReferenceIdx() != "ref-1" {
		t.Fatalf("ReferenceIdx() = %q", m.ReferenceIdx())
	}
	if err := Text("hi").ReplyTo("msg-1", 0).Validate(); err == nil {
		t.Fatal("Validate(seq=0) = nil, want error")
	}
}

func TestSeqForActive(t *testing.T) {
	m := Text("hi").Seq(3)
	if m.SeqNo() != 3 {
		t.Fatalf("SeqNo() = %d, want 3", m.SeqNo())
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate(active seq) error = %v", err)
	}
	if err := Text("hi").Seq(-1).Validate(); err == nil {
		t.Fatal("Validate(negative seq) = nil, want error")
	}
}

func TestCardValidation(t *testing.T) {
	valid := Card{
		Title:       "标题",
		Description: "描述",
		URL:         "https://example.com/a",
		PicURL:      "https://example.com/a.png",
	}
	if err := NewCard(valid).Validate(); err != nil {
		t.Fatalf("Validate(valid card) error = %v", err)
	}

	longTitle := strings.Repeat("标", maxTitleRunes+1)
	longDesc := strings.Repeat("描", maxDescRunes+1)
	tests := []struct {
		name string
		card Card
		want string
	}{
		{name: "empty title", card: Card{Description: "d", URL: "https://e.com", PicURL: "https://e.com/p"}, want: "title"},
		{name: "long title", card: Card{Title: longTitle, Description: "d", URL: "https://e.com", PicURL: "https://e.com/p"}, want: "title"},
		{name: "long desc", card: Card{Title: "t", Description: longDesc, URL: "https://e.com", PicURL: "https://e.com/p"}, want: "desc"},
		{name: "http url", card: Card{Title: "t", Description: "d", URL: "http://e.com", PicURL: "https://e.com/p"}, want: "url"},
		{name: "http pic", card: Card{Title: "t", Description: "d", URL: "https://e.com", PicURL: "http://e.com/p"}, want: "pic_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewCard(tt.card).Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want error mentioning %q", err, tt.want)
			}
		})
	}
}

func TestKeyboardValidation(t *testing.T) {
	ok := Keyboard{Rows: [][]Button{{{ID: "1", Label: "按钮", ActionType: 2, ActionData: "/help"}}}}
	if err := Text("x").Keyboard(ok).Validate(); err != nil {
		t.Fatalf("Validate(valid keyboard) error = %v", err)
	}

	tests := []struct {
		name string
		kb   Keyboard
		want string
	}{
		{name: "no rows", kb: Keyboard{}, want: "行数"},
		{name: "empty row", kb: Keyboard{Rows: [][]Button{{}}}, want: "按钮数"},
		{name: "empty label", kb: Keyboard{Rows: [][]Button{{{ID: "1"}}}}, want: "label"},
		{name: "link missing", kb: Keyboard{Rows: [][]Button{{{ID: "1", Label: "去", ActionType: 0}}}}, want: "link"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Text("x").Keyboard(tt.kb).Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want error mentioning %q", err, tt.want)
			}
		})
	}
}

func TestMediaValidation(t *testing.T) {
	if err := Media("file-info").Validate(); err != nil {
		t.Fatalf("Validate(media) error = %v", err)
	}
	if err := Media("").Validate(); err == nil {
		t.Fatal("Validate(empty media) = nil, want error")
	}
}

func TestButtonModalValidation(t *testing.T) {
	ok := Text("hi").Keyboard(Keyboard{Rows: [][]Button{{{ID: "b1", Label: "确认", ActionType: 1, ActionData: "go",
		Modal: &Modal{Content: "确定要执行吗？", ConfirmText: "确定", CancelText: "取消"}}}}})
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate(modal ok) error = %v", err)
	}
	bad := Text("hi").Keyboard(Keyboard{Rows: [][]Button{{{ID: "b1", Label: "x", ActionType: 1, ActionData: "go",
		Modal: &Modal{Content: "看 https://example.com 这个"}}}}})
	if err := bad.Validate(); err == nil {
		t.Fatal("Validate(modal with URL) = nil, want error")
	}
	long := Text("hi").Keyboard(Keyboard{Rows: [][]Button{{{ID: "b1", Label: "x", ActionType: 1, ActionData: "go",
		Modal: &Modal{Content: strings.Repeat("长", 41)}}}}})
	if err := long.Validate(); err == nil {
		t.Fatal("Validate(modal content >40) = nil, want error")
	}
}

func TestMarkdownTemplateValidation(t *testing.T) {
	m := NewMarkdownTemplate("65", MarkdownParam{Key: "title", Values: []string{"标题"}})
	if m.Kind() != KindMarkdown || m.MarkdownTemplateData() == nil {
		t.Fatalf("NewMarkdownTemplate() = kind=%v tpl=%v", m.Kind(), m.MarkdownTemplateData())
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate(valid template) error = %v", err)
	}
	if err := NewMarkdownTemplate("").Validate(); err == nil {
		t.Fatal("Validate(empty id) = nil, want error")
	}
	if err := NewMarkdownTemplate("65", MarkdownParam{Values: []string{"x"}}).Validate(); err == nil {
		t.Fatal("Validate(empty key) = nil, want error")
	}
}

func TestReplyAnchorsMutuallyExclusive(t *testing.T) {
	if err := Text("x").ReplyTo("m-1", 1).ReplyToEvent("ev-1", 1).Validate(); err == nil {
		t.Fatal("Validate(msg_id + event_id) = nil, want error")
	}
	if err := Text("x").ReplyToEvent("ev-1", 1).Validate(); err != nil {
		t.Fatalf("Validate(event anchor) error = %v", err)
	}
	evID, seq := Text("x").ReplyToEvent("ev-1", 2).ReplyEvent()
	if evID != "ev-1" || seq != 2 {
		t.Fatalf("ReplyEvent() = %q/%d", evID, seq)
	}
}

func TestKeyboardAndTemplateMutuallyExclusive(t *testing.T) {
	kb := Keyboard{Rows: [][]Button{{{ID: "1", Label: "按钮", ActionType: 2, ActionData: "/x"}}}}
	if err := Text("x").Keyboard(kb).KeyboardTemplate("kb-1").Validate(); err == nil {
		t.Fatal("Validate(keyboard + template id) = nil, want error")
	}
	if err := Text("x").KeyboardTemplate("kb-1").Validate(); err != nil {
		t.Fatalf("Validate(keyboard template) error = %v", err)
	}
}
