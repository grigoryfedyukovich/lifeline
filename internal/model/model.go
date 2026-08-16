package model

// Span is parser-independent source metadata. Offsets are byte offsets in the
// source file; lines and columns are one-based.
type Span struct {
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	StartOffset int    `json:"start_offset,omitempty"`
	EndOffset   int    `json:"end_offset,omitempty"`
}

type Evidence struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Span    *Span  `json:"span,omitempty"`
}

type FixEdit struct {
	Span    Span   `json:"span"`
	NewText string `json:"new_text"`
}

type SuggestedFix struct {
	Message string    `json:"message"`
	Edits   []FixEdit `json:"edits"`
}

// Instruction is Lifeline's parser-independent SSA-like instruction summary.
// It deliberately records only information needed by lifecycle recognizers and
// diagnostics; it is not a general Go SSA representation.
type Instruction struct {
	Index   int      `json:"index"`
	Op      string   `json:"op"`
	Span    Span     `json:"span"`
	Callee  string   `json:"callee,omitempty"`
	Defines []string `json:"defines,omitempty"`
	Uses    []string `json:"uses,omitempty"`
}

type CancelBinding struct {
	Factory      string        `json:"factory"`
	ContextName  string        `json:"context_name,omitempty"`
	CancelName   string        `json:"cancel_name,omitempty"`
	Span         Span          `json:"span"`
	Discarded    bool          `json:"discarded"`
	Called       bool          `json:"called"`
	Escapes      bool          `json:"escapes"`
	UsedByChild  bool          `json:"used_by_child"`
	Evidence     []Evidence    `json:"evidence,omitempty"`
	SuggestedFix *SuggestedFix `json:"suggested_fix,omitempty"`
}

type Goroutine struct {
	Span              Span       `json:"span"`
	Kind              string     `json:"kind"`
	InfiniteLoop      bool       `json:"infinite_loop"`
	HasReturn         bool       `json:"has_return"`
	ContextStop       bool       `json:"context_stop"`
	ChannelStop       bool       `json:"channel_stop"`
	ExplicitStop      bool       `json:"explicit_stop"`
	AvailableContexts []string   `json:"available_contexts,omitempty"`
	CapturedNames     []string   `json:"captured_names,omitempty"`
	Evidence          []Evidence `json:"evidence,omitempty"`
}

type JoinGroup struct {
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Span     Span       `json:"span"`
	Starts   int        `json:"starts"`
	Joined   bool       `json:"joined"`
	Escapes  bool       `json:"escapes"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type Function struct {
	Name          string          `json:"name"`
	Span          Span            `json:"span"`
	Contexts      []string        `json:"contexts,omitempty"`
	Cancels       []CancelBinding `json:"cancels,omitempty"`
	Goroutines    []Goroutine     `json:"goroutines,omitempty"`
	Groups        []JoinGroup     `json:"groups,omitempty"`
	BodyLifecycle Goroutine       `json:"body_lifecycle"`
	IR            []Instruction   `json:"ir,omitempty"`
}

type Program struct {
	PackagePath   string     `json:"package_path"`
	Functions     []Function `json:"functions"`
	FunctionCount int        `json:"function_count"`
	Truncated     bool       `json:"truncated"`
}
