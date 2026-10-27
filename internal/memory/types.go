// Package memory defines project-memory contracts and database-independent rules.
package memory

import (
	"context"
	"encoding/json"
	"projectmap/internal/access"
)

type Record map[string]any

type Error struct {
	Status        int
	Code, Message string
	Details       any
}

func (e *Error) Error() string { return e.Message }
func Problem(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}
func Invalid(path, message string) *Error {
	return &Error{Status: 422, Code: "validation_failed", Message: "Project input is invalid.", Details: map[string]any{"fields": []map[string]string{{"path": path, "message": message}}}}
}

type OverviewInput struct {
	Title   string  `json:"title"`
	Content *string `json:"content"`
}
type QuestionInput struct {
	Question string `json:"question"`
}
type DecisionInput struct {
	Title           string  `json:"title"`
	Statement       string  `json:"statement"`
	Rationale       string  `json:"rationale"`
	SourceReference *string `json:"source_reference"`
}
type WorkspaceInput struct {
	WorkspaceKey  string  `json:"workspace_key"`
	RepositoryURL *string `json:"repository_url"`
}
type CheckpointInput struct {
	Summary         string  `json:"summary"`
	SourceReference *string `json:"source_reference"`
}
type CreateRequest struct {
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	NextStep   string          `json:"next_step"`
	Overview   *OverviewInput  `json:"overview"`
	Questions  []QuestionInput `json:"questions"`
	Decisions  []DecisionInput `json:"decisions"`
	Workspace  *WorkspaceInput `json:"workspace"`
	Checkpoint CheckpointInput `json:"checkpoint"`
}
type Operation struct {
	Ref  string                     `json:"ref"`
	Type string                     `json:"type"`
	ID   string                     `json:"id,omitempty"`
	Data map[string]json.RawMessage `json:"data,omitempty"`
}
type UpdateRequest struct {
	ExpectedRevision int64       `json:"expected_revision"`
	Summary          string      `json:"summary"`
	SourceReference  *string     `json:"source_reference"`
	Operations       []Operation `json:"operations"`
}
type WriteRequest struct {
	ProjectID, Key, Hash string
	Create               *CreateRequest
	Update               *UpdateRequest
}
type WriteResult struct {
	Status   int
	Body     json.RawMessage
	Replayed bool
}
type Page struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}
type Envelope struct {
	Data any    `json:"data"`
	Page *Page  `json:"page,omitempty"`
	Meta Record `json:"meta,omitempty"`
}
type Query struct {
	Resource                                    string
	ProjectID, TargetID                         string
	Search, Archived, Kind, State, WorkspaceKey string
	Limit                                       int
	Cursor                                      string
}
type Backend interface {
	Read(context.Context, access.Identity, Query) (Envelope, error)
	Write(context.Context, access.Identity, WriteRequest) (WriteResult, error)
}
type Service struct{ Backend Backend }
