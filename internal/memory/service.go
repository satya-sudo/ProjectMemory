package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"projectmap/internal/access"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidID(id string) bool { return uuidPattern.MatchString(id) }
func validText(value, path string, max int, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return Invalid(path, "Use valid text without NUL characters.")
	}
	if required && strings.TrimSpace(value) == "" {
		return Invalid(path, "A nonblank value is required.")
	}
	if utf8.RuneCountInString(value) > max {
		return Invalid(path, "Value exceeds its length limit.")
	}
	return nil
}
func optionalText(value *string, path string, max int) error {
	if value == nil {
		return nil
	}
	return validText(*value, path, max, false)
}
func validWorkspace(value, path string) error {
	if err := validText(value, path, 4096, true); err != nil {
		return err
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return Invalid(path, "Use a canonical absolute local path.")
	}
	return nil
}
func ValidKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, v := range []byte(key) {
		if v < 33 || v > 126 {
			return false
		}
	}
	return true
}
func canonicalHash(operation, project string, body any) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", Problem(400, "invalid_request", "Invalid JSON input.")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return "", Problem(400, "invalid_request", "Invalid JSON input.")
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", Problem(400, "invalid_request", "Invalid JSON input.")
	}
	hash := sha256.Sum256(append([]byte(operation+":"+project+":"), canonical...))
	return hex.EncodeToString(hash[:]), nil
}
func (s *Service) Create(ctx context.Context, actor access.Identity, key string, request CreateRequest) (WriteResult, error) {
	if s.Backend == nil {
		return WriteResult{}, Problem(503, "service_unavailable", "The project memory service is unavailable.")
	}
	if !ValidKey(key) {
		return WriteResult{}, Problem(400, "invalid_request", "Supply one valid Idempotency-Key.")
	}
	if err := ValidateCreate(&request); err != nil {
		return WriteResult{}, err
	}
	hash, err := canonicalHash("create", "", request)
	if err != nil {
		return WriteResult{}, err
	}
	return s.Backend.Write(ctx, actor, WriteRequest{Key: key, Hash: hash, Create: &request})
}
func (s *Service) Update(ctx context.Context, actor access.Identity, project, key string, request UpdateRequest) (WriteResult, error) {
	if s.Backend == nil {
		return WriteResult{}, Problem(503, "service_unavailable", "The project memory service is unavailable.")
	}
	if !ValidID(project) {
		return WriteResult{}, Problem(400, "invalid_request", "Invalid project identifier.")
	}
	if !ValidKey(key) {
		return WriteResult{}, Problem(400, "invalid_request", "Supply one valid Idempotency-Key.")
	}
	if err := ValidateUpdate(request); err != nil {
		return WriteResult{}, err
	}
	request.Operations = append([]Operation(nil), request.Operations...)
	for i := range request.Operations {
		request.Operations[i].ID = strings.ToLower(request.Operations[i].ID)
	}
	project = strings.ToLower(project)
	hash, err := canonicalHash("update", project, request)
	if err != nil {
		return WriteResult{}, err
	}
	return s.Backend.Write(ctx, actor, WriteRequest{ProjectID: project, Key: key, Hash: hash, Update: &request})
}
func (s *Service) Read(ctx context.Context, actor access.Identity, query Query) (Envelope, error) {
	if s.Backend == nil {
		return Envelope{}, Problem(503, "service_unavailable", "The project memory service is unavailable.")
	}
	if err := ValidateQuery(&query); err != nil {
		return Envelope{}, err
	}
	return s.Backend.Read(ctx, actor, query)
}
func ValidateCreate(request *CreateRequest) error {
	for _, f := range []struct {
		value, path string
		max         int
		required    bool
	}{{request.Title, "title", 200, true}, {request.Summary, "summary", 2000, false}, {request.NextStep, "next_step", 2000, false}, {request.Checkpoint.Summary, "checkpoint.summary", 2000, true}} {
		if err := validText(f.value, f.path, f.max, f.required); err != nil {
			return err
		}
	}
	if err := optionalText(request.Checkpoint.SourceReference, "checkpoint.source_reference", 4096); err != nil {
		return err
	}
	if request.Overview == nil || request.Overview.Content == nil {
		return Invalid("overview", "An overview title and content are required.")
	}
	if err := validText(request.Overview.Title, "overview.title", 200, true); err != nil {
		return err
	}
	if err := validText(*request.Overview.Content, "overview.content", 256<<10, false); err != nil {
		return err
	}
	if len(*request.Overview.Content) > 256<<10 {
		return Invalid("overview.content", "Content exceeds 256 KiB.")
	}
	if len(request.Questions) > 20 || len(request.Decisions) > 20 {
		return Invalid("questions", "At most 20 initial questions and decisions are allowed each.")
	}
	for i, q := range request.Questions {
		if err := validText(q.Question, "questions["+strconv.Itoa(i)+"].question", 8000, true); err != nil {
			return err
		}
	}
	for i, d := range request.Decisions {
		prefix := "decisions[" + strconv.Itoa(i) + "]."
		for _, f := range []struct {
			value, name string
			max         int
			required    bool
		}{{d.Title, "title", 200, true}, {d.Statement, "statement", 8000, true}, {d.Rationale, "rationale", 8000, false}} {
			if err := validText(f.value, prefix+f.name, f.max, f.required); err != nil {
				return err
			}
		}
		if err := optionalText(d.SourceReference, prefix+"source_reference", 4096); err != nil {
			return err
		}
	}
	if request.Workspace != nil {
		if err := validWorkspace(request.Workspace.WorkspaceKey, "workspace.workspace_key"); err != nil {
			return err
		}
		if err := optionalText(request.Workspace.RepositoryURL, "workspace.repository_url", 4096); err != nil {
			return err
		}
	}
	if request.Questions == nil {
		request.Questions = []QuestionInput{}
	}
	if request.Decisions == nil {
		request.Decisions = []DecisionInput{}
	}
	return nil
}
func ValidateUpdate(request UpdateRequest) error {
	if request.ExpectedRevision < 1 {
		return Invalid("expected_revision", "A positive project revision is required.")
	}
	if err := validText(request.Summary, "summary", 2000, true); err != nil {
		return err
	}
	if err := optionalText(request.SourceReference, "source_reference", 4096); err != nil {
		return err
	}
	if len(request.Operations) == 0 || len(request.Operations) > 50 {
		return Invalid("operations", "Supply between 1 and 50 operations.")
	}
	refs := map[string]bool{}
	targets := map[string]string{}
	for i, op := range request.Operations {
		path := "operations[" + strconv.Itoa(i) + "]"
		if err := validText(op.Ref, path+".ref", 64, true); err != nil {
			return err
		}
		if refs[op.Ref] {
			return Invalid(path+".ref", "Operation references must be unique.")
		}
		refs[op.Ref] = true
		spec, ok := operationSpecs[op.Type]
		if !ok {
			return Problem(400, "invalid_request", "Unknown operation type.")
		}
		if (op.Type == "project.archive" || op.Type == "project.unarchive") && len(request.Operations) != 1 {
			return Invalid(path, "Archive/unarchive must be the only operation.")
		}
		if spec.existing {
			if !ValidID(op.ID) {
				return Problem(400, "invalid_request", "An existing target requires a valid identifier.")
			}
		} else if op.ID != "" {
			return Problem(400, "invalid_request", "This operation does not accept a target identifier.")
		}
		if spec.existing || spec.entity == "project" {
			key := spec.entity + ":" + strings.ToLower(op.ID)
			if previous, exists := targets[key]; exists {
				paired := (previous == "document.update" && op.Type == "document.set_state") || (previous == "decision.update" && op.Type == "decision.set_state")
				if !paired {
					return Invalid(path, "Duplicate operations on the same target are not allowed.")
				}
			}
			targets[key] = op.Type
		}
		if _, err := parseData(op, path+".data"); err != nil {
			return err
		}
	}
	return nil
}
func ValidateQuery(query *Query) error {
	if (query.Kind != "" && query.Resource != "documents") || (query.State != "" && !contains([]string{"documents", "decisions", "questions"}, query.Resource)) || ((query.Search != "" || query.Archived != "") && query.Resource != "projects") || (query.WorkspaceKey != "" && query.Resource != "resolve") {
		return Problem(400, "invalid_request", "Filter does not apply to this resource.")
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 || query.Limit > 100 {
		return Invalid("limit", "Limit must be between 1 and 100.")
	}
	if len(query.Cursor) > 2048 {
		return Problem(400, "invalid_cursor", "Invalid cursor.")
	}
	if query.ProjectID != "" {
		if !ValidID(query.ProjectID) {
			return Problem(400, "invalid_request", "Invalid project identifier.")
		}
		query.ProjectID = strings.ToLower(query.ProjectID)
	}
	if query.TargetID != "" {
		if !ValidID(query.TargetID) {
			return Problem(400, "invalid_request", "Invalid resource identifier.")
		}
		query.TargetID = strings.ToLower(query.TargetID)
	}
	switch query.Resource {
	case "projects":
		if query.Archived == "" {
			query.Archived = "false"
		}
		if !contains([]string{"false", "true", "all"}, query.Archived) {
			return Problem(400, "invalid_request", "Invalid archived filter.")
		}
		return validText(query.Search, "q", 200, false)
	case "resolve":
		return validWorkspace(query.WorkspaceKey, "workspace_key")
	case "project", "context", "document", "checkpoint":
	case "documents":
		if query.Kind != "" && !contains([]string{"overview", "feature_plan", "architecture", "note"}, query.Kind) {
			return Problem(400, "invalid_request", "Invalid document kind.")
		}
		if query.State != "" && !contains([]string{"draft", "accepted", "superseded"}, query.State) {
			return Problem(400, "invalid_request", "Invalid document state.")
		}
	case "decisions":
		if query.State != "" && !contains([]string{"proposed", "accepted", "rejected", "superseded"}, query.State) {
			return Problem(400, "invalid_request", "Invalid decision state.")
		}
	case "questions":
		if query.State != "" && !contains([]string{"open", "resolved", "dismissed"}, query.State) {
			return Problem(400, "invalid_request", "Invalid question state.")
		}
	case "progress", "bindings", "history":
	default:
		return Problem(400, "invalid_request", "Unknown read operation.")
	}
	if query.ProjectID == "" {
		return Problem(400, "invalid_request", "A project identifier is required.")
	}
	if (query.Resource == "document" || query.Resource == "checkpoint") && query.TargetID == "" {
		return Problem(400, "invalid_request", "A resource identifier is required.")
	}
	return nil
}
func contains(options []string, value string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

// Preview bounds UTF-8 bytes without splitting a character.
func Preview(content string, limit int) (string, bool) {
	if len(content) <= limit {
		return content, false
	}
	if limit < 0 {
		limit = 0
	}
	for limit > 0 && !utf8.RuneStart(content[limit]) {
		limit--
	}
	return content[:limit], true
}
