package docsite

// An OpenAPI 3 document rendered as a markdown page, for a hook with
// format = "openapi". Deliberately plain: every operation, its parameters,
// body, answers and security, and every schema's properties, in the order the
// document gives paths and sorted where it gives a map. A project that wants
// more renders its own markdown and uses a markdown hook instead.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type openAPIDoc struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title       string `json:"title"`
		Version     string `json:"version"`
		Description string `json:"description"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Security   []map[string][]string                 `json:"security"`
	Components struct {
		Schemas         map[string]*openAPISchema `json:"schemas"`
		SecuritySchemes map[string]struct {
			Type        string `json:"type"`
			Scheme      string `json:"scheme"`
			In          string `json:"in"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"securitySchemes"`
	} `json:"components"`
}

type openAPIOperation struct {
	OperationID string             `json:"operationId"`
	Summary     string             `json:"summary"`
	Description string             `json:"description"`
	Deprecated  bool               `json:"deprecated"`
	Parameters  []openAPIParameter `json:"parameters"`
	RequestBody *struct {
		Description string                      `json:"description"`
		Required    bool                        `json:"required"`
		Content     map[string]openAPIMediaType `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Description string                      `json:"description"`
		Content     map[string]openAPIMediaType `json:"content"`
	} `json:"responses"`
	Security *[]map[string][]string `json:"security"`
}

type openAPIParameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Schema      *openAPISchema `json:"schema"`
}

type openAPIMediaType struct {
	Schema *openAPISchema `json:"schema"`
}

type openAPISchema struct {
	Ref                  string                    `json:"$ref"`
	Type                 any                       `json:"type"`
	Format               string                    `json:"format"`
	Description          string                    `json:"description"`
	Items                *openAPISchema            `json:"items"`
	Properties           map[string]*openAPISchema `json:"properties"`
	Required             []string                  `json:"required"`
	Enum                 []any                     `json:"enum"`
	AnyOf                []*openAPISchema          `json:"anyOf"`
	OneOf                []*openAPISchema          `json:"oneOf"`
	AdditionalProperties any                       `json:"additionalProperties"`
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// OpenAPIMarkdown renders an OpenAPI 3 JSON document as markdown.
func OpenAPIMarkdown(raw []byte) ([]byte, error) {
	var d openAPIDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("not an OpenAPI JSON document: %w", err)
	}
	if !strings.HasPrefix(d.OpenAPI, "3.") {
		return nil, fmt.Errorf("openapi version %q: only OpenAPI 3 is rendered", d.OpenAPI)
	}
	if len(d.Paths) == 0 {
		return nil, fmt.Errorf("the document has no paths; a page listing no endpoints would read as an answer")
	}

	type op struct {
		method, path, heading string
		op                    openAPIOperation
	}
	var ops []op
	paths := make([]string, 0, len(d.Paths))
	for p := range d.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, m := range httpMethods {
			rawOp, ok := d.Paths[p][m]
			if !ok {
				continue
			}
			var o openAPIOperation
			if err := json.Unmarshal(rawOp, &o); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(m), p, err)
			}
			heading := strings.ToUpper(m) + " " + p
			ops = append(ops, op{m, p, heading, o})
		}
	}

	var b strings.Builder
	title := d.Info.Title
	if title == "" {
		title = "API"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if d.Info.Description != "" {
		b.WriteString(strings.TrimSpace(d.Info.Description) + "\n\n")
	}
	if d.Info.Version != "" {
		fmt.Fprintf(&b, "Version `%s`, OpenAPI %s.\n\n", d.Info.Version, d.OpenAPI)
	}

	links := map[string]int{}
	b.WriteString("## Endpoints\n\n| endpoint | summary | auth |\n|---|---|---|\n")
	for _, o := range ops {
		fmt.Fprintf(&b, "| [`%s`](#%s) | %s | %s |\n", o.heading, headingID(o.heading, links), cell(o.op.Summary), security(o.op.Security, d.Security))
	}

	if len(d.Components.SecuritySchemes) > 0 {
		b.WriteString("\n## Authentication\n\n| scheme | type | description |\n|---|---|---|\n")
		for _, name := range sortedKeys(d.Components.SecuritySchemes) {
			s := d.Components.SecuritySchemes[name]
			kind := s.Type
			switch {
			case s.Scheme != "":
				kind += " (" + s.Scheme + ")"
			case s.In != "":
				kind += " (`" + s.Name + "` in " + s.In + ")"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, kind, cell(s.Description))
		}
	}

	b.WriteString("\n## Every endpoint\n\n")
	for _, o := range ops {
		fmt.Fprintf(&b, "### %s\n\n", o.heading)
		if o.op.Deprecated {
			b.WriteString("**Deprecated.** ")
		}
		if s := strings.TrimSpace(o.op.Summary); s != "" {
			b.WriteString(sentence(s) + "\n\n")
		}
		if s := strings.TrimSpace(o.op.Description); s != "" {
			b.WriteString(s + "\n\n")
		}
		if o.op.OperationID != "" {
			fmt.Fprintf(&b, "- **Operation:** `%s`\n", o.op.OperationID)
		}
		fmt.Fprintf(&b, "- **Auth:** %s\n", security(o.op.Security, d.Security))
		for _, p := range o.op.Parameters {
			req := ""
			if p.Required {
				req = ", required"
			}
			fmt.Fprintf(&b, "- **`%s`** (%s%s%s): %s\n", p.Name, p.In, typeSuffix(p.Schema), req, oneLine(p.Description))
		}
		if rb := o.op.RequestBody; rb != nil {
			fmt.Fprintf(&b, "- **Body:** %s", media(rb.Content))
			if rb.Description != "" {
				b.WriteString(". " + oneLine(rb.Description))
			}
			b.WriteString("\n")
		}
		var answers []string
		for _, code := range sortedKeys(o.op.Responses) {
			r := o.op.Responses[code]
			a := code
			if r.Description != "" {
				a += " " + oneLine(r.Description)
			}
			if len(r.Content) > 0 {
				a += " (" + media(r.Content) + ")"
			}
			answers = append(answers, a)
		}
		if len(answers) > 0 {
			fmt.Fprintf(&b, "- **Answers:** %s\n", strings.Join(answers, "; "))
		}
		b.WriteString("\n")
	}

	if len(d.Components.Schemas) > 0 {
		b.WriteString("## Schemas\n\n")
		for _, name := range sortedKeys(d.Components.Schemas) {
			s := d.Components.Schemas[name]
			fmt.Fprintf(&b, "### %s\n\n", name)
			if s.Description != "" {
				b.WriteString(strings.TrimSpace(s.Description) + "\n\n")
			}
			if len(s.Properties) == 0 {
				fmt.Fprintf(&b, "%s.\n\n", sentence(typeName(s)))
				continue
			}
			required := map[string]bool{}
			for _, r := range s.Required {
				required[r] = true
			}
			b.WriteString("| property | type | required | description |\n|---|---|---|---|\n")
			for _, pn := range sortedKeys(s.Properties) {
				p := s.Properties[pn]
				req := ""
				if required[pn] {
					req = "yes"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", pn, typeName(p), req, cell(p.Description))
			}
			b.WriteString("\n")
		}
	}
	return []byte(b.String()), nil
}

// headingID is the id goldmark's parser gives a heading of this text, so a
// link can be written before the heading is rendered. The anchor check fails
// the build if this ever disagrees.
func headingID(text string, seen map[string]int) string {
	var id []byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= 0x80:
			continue
		case c >= 'A' && c <= 'Z':
			id = append(id, c+'a'-'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			id = append(id, c)
		case c == ' ' || c == '\t' || c == '-' || c == '_':
			id = append(id, '-')
		}
	}
	s := string(id)
	if s == "" {
		s = "heading"
	}
	n, dup := seen[s]
	seen[s] = n + 1
	if dup {
		return fmt.Sprintf("%s-%d", s, n)
	}
	return s
}

func security(op *[]map[string][]string, global []map[string][]string) string {
	reqs := global
	if op != nil {
		reqs = *op
	}
	var names []string
	for _, r := range reqs {
		for _, k := range sortedKeys(r) {
			names = append(names, "`"+k+"`")
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, " or ")
}

func media(content map[string]openAPIMediaType) string {
	var out []string
	for _, ct := range sortedKeys(content) {
		s := "`" + ct + "`"
		if sc := content[ct].Schema; sc != nil {
			if t := typeName(sc); t != "" {
				s += ", " + t
			}
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

func typeSuffix(s *openAPISchema) string {
	if t := typeName(s); t != "" {
		return ", " + t
	}
	return ""
}

// typeName is a schema in a few words: a $ref's name, an array of something,
// or the type and format.
func typeName(s *openAPISchema) string {
	if s == nil {
		return ""
	}
	if s.Ref != "" {
		return "`" + s.Ref[strings.LastIndex(s.Ref, "/")+1:] + "`"
	}
	for _, alts := range [][]*openAPISchema{s.AnyOf, s.OneOf} {
		if len(alts) > 0 {
			var names []string
			for _, a := range alts {
				names = append(names, typeName(a))
			}
			return strings.Join(names, " or ")
		}
	}
	var t string
	switch v := s.Type.(type) {
	case string:
		t = v
	case []any:
		var parts []string
		for _, p := range v {
			parts = append(parts, fmt.Sprint(p))
		}
		t = strings.Join(parts, " or ")
	}
	switch {
	case t == "array" && s.Items != nil:
		return "array of " + typeName(s.Items)
	case t == "object" && s.AdditionalProperties != nil:
		if ap, ok := s.AdditionalProperties.(map[string]any); ok {
			b, _ := json.Marshal(ap)
			var inner openAPISchema
			if json.Unmarshal(b, &inner) == nil {
				return "map of " + typeName(&inner)
			}
		}
	}
	if s.Format != "" {
		t += " (" + s.Format + ")"
	}
	if len(s.Enum) > 0 {
		var vals []string
		for _, e := range s.Enum {
			vals = append(vals, fmt.Sprintf("`%v`", e))
		}
		t += ": one of " + strings.Join(vals, ", ")
	}
	return t
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// oneLine joins a multi-line description for a list item.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cell is oneLine with the table's column separator escaped.
func cell(s string) string { return strings.ReplaceAll(oneLine(s), "|", `\|`) }

// sentence capitalises s and ends it with a full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
