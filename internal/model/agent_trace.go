package model

// AgentTraceEvent describes an observable action, never private model reasoning
// or HTTP credentials. A stable Key pairs the beginning and end of one action.
// Payloads stay in separate execution files rather than the polled State.
type AgentTraceEvent struct {
	Key        string `json:"key"`
	Phase      string `json:"phase"`
	Session    int    `json:"session"`
	Turn       int    `json:"turn"`
	Kind       string `json:"kind"`
	Tool       string `json:"tool"`
	CallID     string `json:"callId"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	Arguments  string `json:"arguments"`
	Output     string `json:"output"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	Usage      Usage  `json:"usage"`
}

// AgentTraceEntry is a lightweight row; arguments and tool output are only
// fetched after the user explicitly expands those details.
type AgentTraceEntry struct {
	Sequence       int    `json:"sequence"`
	Iteration      int    `json:"iteration"`
	Session        int    `json:"session"`
	Turn           int    `json:"turn"`
	Phase          string `json:"phase"`
	Kind           string `json:"kind"`
	Tool           string `json:"tool"`
	CallID         string `json:"callId"`
	Status         string `json:"status"`
	Summary        string `json:"summary"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt"`
	ArgumentsBytes int    `json:"argumentsBytes"`
	OutputBytes    int    `json:"outputBytes"`
	Usage          Usage  `json:"usage"`
}

type AgentTracePage struct {
	Entries    []AgentTraceEntry `json:"entries"`
	NextOffset int               `json:"nextOffset"`
	Total      int               `json:"total"`
	Complete   bool              `json:"complete"`
}

type AgentTraceContent struct {
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"nextOffset"`
	TotalBytes int    `json:"totalBytes"`
	Complete   bool   `json:"complete"`
}
