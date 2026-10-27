package memory

import (
	"bytes"
	"encoding/json"
	"projectmap/internal/jsonutil"
	"strconv"
	"strings"
	"time"
)

type fieldSpec struct {
	max                          int
	required, nonblank, nullable bool
	choices                      []string
	evidence                     bool
}
type operationSpec struct {
	entity   string
	existing bool
	fields   map[string]fieldSpec
}

func text(max int, required, nonblank bool) fieldSpec {
	return fieldSpec{max: max, required: required, nonblank: nonblank}
}
func nullable(max int) fieldSpec { return fieldSpec{max: max, nullable: true} }
func enum(required bool, choices ...string) fieldSpec {
	return fieldSpec{required: required, choices: choices}
}

var operationSpecs = map[string]operationSpec{
	"project.update":  {"project", false, map[string]fieldSpec{"title": text(200, false, true), "summary": text(2000, false, false), "next_step": text(2000, false, false)}},
	"project.archive": {"project", false, nil}, "project.unarchive": {"project", false, nil},
	"document.create":    {"document", false, map[string]fieldSpec{"kind": enum(true, "overview", "feature_plan", "architecture", "note"), "title": text(200, true, true), "content": text(256<<10, true, false)}},
	"document.update":    {"document", true, map[string]fieldSpec{"title": text(200, false, true), "content": text(256<<10, false, false)}},
	"document.set_state": {"document", true, map[string]fieldSpec{"state": enum(true, "draft", "accepted", "superseded"), "acceptance_reference": text(4096, false, true)}},
	"decision.create":    {"decision", false, map[string]fieldSpec{"title": text(200, true, true), "statement": text(8000, true, true), "rationale": text(8000, false, false), "source_reference": nullable(4096)}},
	"decision.update":    {"decision", true, map[string]fieldSpec{"title": text(200, false, true), "statement": text(8000, false, true), "rationale": text(8000, false, false), "source_reference": nullable(4096)}},
	"decision.set_state": {"decision", true, map[string]fieldSpec{"state": enum(true, "proposed", "accepted", "rejected", "superseded"), "acceptance_reference": text(4096, false, true)}},
	"question.create":    {"question", false, map[string]fieldSpec{"question": text(8000, true, true)}},
	"question.update":    {"question", true, map[string]fieldSpec{"question": text(8000, false, true), "state": enum(false, "open", "resolved", "dismissed"), "resolution": nullable(8000)}},
	"progress.create":    {"progress", false, map[string]fieldSpec{"summary": text(8000, true, true), "verification": enum(false, "reported", "verified"), "evidence": {evidence: true}}},
	"progress.update":    {"progress", true, map[string]fieldSpec{"summary": text(8000, false, true), "verification": enum(false, "reported", "verified"), "evidence": {evidence: true}}},
	"workspace.bind":     {"workspace_binding", false, map[string]fieldSpec{"workspace_key": text(4096, true, true), "repository_url": nullable(4096)}},
	"workspace.unbind":   {"workspace_binding", true, nil},
}

func EntityFor(operation string) string { return operationSpecs[operation].entity }
func Existing(operation string) bool    { return operationSpecs[operation].existing }
func parseData(op Operation, path string) (Record, error) {
	spec, ok := operationSpecs[op.Type]
	if !ok {
		return nil, Problem(400, "invalid_request", "Unknown operation type.")
	}
	out := Record{}
	for name, raw := range op.Data {
		field, ok := spec.fields[name]
		if !ok {
			return nil, Problem(400, "invalid_request", "Unknown operation data field.")
		}
		key := path + "." + name
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if field.nullable {
				out[name] = nil
				continue
			}
			return nil, Problem(400, "invalid_request", "A non-null field has an invalid value.")
		}
		if field.evidence {
			var evidence []Evidence
			if err := jsonutil.Decode(raw, &evidence); err != nil {
				return nil, Problem(400, "invalid_request", "Invalid evidence shape.")
			}
			if len(evidence) > 20 {
				return nil, Invalid(key, "At most 20 evidence references are allowed.")
			}
			for i, item := range evidence {
				prefix := key + "[" + strconv.Itoa(i) + "]."
				if err := validText(item.Kind, prefix+"kind", 64, true); err != nil {
					return nil, err
				}
				if err := validText(item.Reference, prefix+"reference", 4096, true); err != nil {
					return nil, err
				}
				if err := validText(item.Description, prefix+"description", 2000, false); err != nil {
					return nil, err
				}
			}
			if evidence == nil {
				evidence = []Evidence{}
			}
			out[name] = evidence
			continue
		}
		var value string
		if err := jsonutil.Decode(raw, &value); err != nil {
			return nil, Problem(400, "invalid_request", "Invalid operation field type.")
		}
		if field.choices != nil {
			if !contains(field.choices, value) {
				return nil, Invalid(key, "Unknown state or kind.")
			}
		} else if err := validText(value, key, field.max, field.nonblank); err != nil {
			return nil, err
		}
		if name == "content" && len(value) > 256<<10 {
			return nil, Invalid(key, "Content exceeds 256 KiB.")
		}
		if name == "workspace_key" {
			if err := validWorkspace(value, key); err != nil {
				return nil, err
			}
		}
		out[name] = value
	}
	for name, field := range spec.fields {
		if field.required {
			if _, ok := out[name]; !ok {
				return nil, Invalid(path+"."+name, "Field is required.")
			}
		}
	}
	if strings.HasSuffix(op.Type, ".update") && len(out) == 0 {
		return nil, Invalid(path, "Supply at least one editable field.")
	}
	return out, nil
}

type Evidence struct {
	Kind        string `json:"kind"`
	Reference   string `json:"reference"`
	Description string `json:"description"`
}

func Clone(record Record) Record {
	out := Record{}
	for key, value := range record {
		out[key] = value
	}
	return out
}
func canonical(value any) []byte {
	encoded, _ := json.Marshal(value)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if decoder.Decode(&generic) != nil {
		return nil
	}
	body, _ := json.Marshal(generic)
	return body
}
func Equal(a, b any) bool { return bytes.Equal(canonical(a), canonical(b)) }
func Int(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case float64:
		return int64(v)
	}
	return 0
}
func transition(message string) error { return Problem(422, "invalid_transition", message) }
func noChange() error {
	return Problem(422, "no_change", "Operation would not change current content.")
}

// Apply operates on copies. Persistence applies a complete batch in one transaction.
func Apply(op Operation, before Record, id, project string, revision int64, now time.Time) (Record, Record, error) {
	data, err := parseData(op, "data")
	if err != nil {
		return nil, nil, err
	}
	entity := EntityFor(op.Type)
	metadata := Record{}
	if op.Type == "workspace.unbind" {
		return nil, metadata, nil
	}
	after := Clone(before)
	if before == nil {
		after = Record{"id": id, "project_id": project, "created_at": now.UTC()}
		switch entity {
		case "document":
			after["content"] = ""
			after["state"] = "draft"
		case "decision":
			after["state"] = "proposed"
			after["rationale"] = ""
			after["source_reference"] = nil
			after["acceptance_reference"] = nil
		case "question":
			after["state"] = "open"
			after["resolution"] = nil
		case "progress":
			after["verification"] = "reported"
			after["evidence"] = []Evidence{}
		case "workspace_binding":
			after["repository_url"] = nil
		}
	}
	state, _ := before["state"].(string)
	if entity == "document" && state == "superseded" {
		return nil, nil, transition("Superseded documents are read-only.")
	}
	if entity == "decision" && (state == "rejected" || state == "superseded") {
		return nil, nil, transition("Rejected or superseded decisions are read-only.")
	}
	for name, value := range data {
		if name != "acceptance_reference" {
			after[name] = value
		}
	}
	switch op.Type {
	case "project.archive":
		if before["archived_at"] != nil {
			return nil, nil, noChange()
		}
		after["archived_at"] = now.UTC()
	case "project.unarchive":
		if before["archived_at"] == nil {
			return nil, nil, noChange()
		}
		after["archived_at"] = nil
	case "document.update", "decision.update":
		if Equal(before, after) {
			return nil, nil, noChange()
		}
		if state == "accepted" {
			if entity == "document" {
				after["state"] = "draft"
			} else {
				after["state"] = "proposed"
				after["acceptance_reference"] = nil
			}
		}
	case "document.set_state", "decision.set_state":
		next := data["state"].(string)
		if next == state {
			return nil, nil, noChange()
		}
		allowed := false
		if entity == "document" {
			allowed = (state == "draft" && (next == "accepted" || next == "superseded")) || (state == "accepted" && (next == "draft" || next == "superseded"))
		} else {
			allowed = (state == "proposed" && (next == "accepted" || next == "rejected" || next == "superseded")) || (state == "accepted" && (next == "proposed" || next == "superseded"))
		}
		if !allowed {
			return nil, nil, transition("This state transition is not allowed.")
		}
		reference, present := data["acceptance_reference"]
		if next == "accepted" {
			if !present {
				return nil, nil, Invalid("acceptance_reference", "Acceptance requires a nonblank reference to user agreement.")
			}
			metadata["acceptance_reference"] = reference
		} else if present {
			return nil, nil, Invalid("acceptance_reference", "An acceptance reference applies only to acceptance.")
		}
		if entity == "decision" {
			after["acceptance_reference"] = nil
			if next == "accepted" {
				after["acceptance_reference"] = reference
			}
		}
	case "question.update":
		if _, changed := data["question"]; changed && !Equal(before["question"], after["question"]) && after["state"] != "open" {
			return nil, nil, transition("Reopen a question before changing its text.")
		}
	case "progress.update":
		_, replacement := data["evidence"]
		if !Equal(before["summary"], after["summary"]) && !replacement {
			after["verification"] = "reported"
			after["evidence"] = []Evidence{}
		}
	}
	if entity == "question" {
		resolution := after["resolution"]
		if after["state"] == "open" {
			if resolution != nil {
				return nil, nil, Invalid("resolution", "Open questions must not have a resolution.")
			}
		} else {
			text, ok := resolution.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, nil, Invalid("resolution", "Resolved or dismissed questions require a resolution.")
			}
		}
	}
	if entity == "progress" && after["verification"] == "verified" {
		var evidence []Evidence
		body, _ := json.Marshal(after["evidence"])
		_ = json.Unmarshal(body, &evidence)
		if len(evidence) == 0 {
			return nil, nil, Invalid("evidence", "Verified progress requires evidence.")
		}
	}
	if before != nil && Equal(before, after) {
		return nil, nil, noChange()
	}
	after["updated_at"] = now.UTC()
	if entity == "project" {
		after["revision"] = revision
	} else {
		after["last_changed_revision"] = revision
	}
	return after, metadata, nil
}
