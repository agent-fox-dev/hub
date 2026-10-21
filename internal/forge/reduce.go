package forge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// delivery is a delivery that has been verified.
type delivery struct {
	// forge is github or gitlab.
	forge string
	slug  string
	// id is the forge's id of the delivery, and event its event name, both as the headers
	// gave them.
	id, event string
	body      []byte
}

// reduction is what a delivery comes to: what the log records of it and what is relayed.
type reduction struct {
	// action is the action of the payload, "" when it has none.
	action string
	// target is the workspace repository the delivery is about, "" for none.
	target  target
	outcome string
	// events are the hub events to relay, in order; only a relayed delivery has any.
	events []audit.HubEvent
}

// errMalformed is a payload that lacks a field its kind needs.
var errMalformed = errors.New("the payload lacks a field its event needs")

// mapper makes the events of a delivery of one kind from its payload. It returns errMalformed
// when the payload lacks what the kind needs, and no event when the payload says nothing that is
// relayed.
type mapper func(b *builder) ([]audit.HubEvent, error)

// reduceDelivery works out what a verified delivery comes to. A body that is not a JSON object,
// or lacks a field its kind needs, is malformed. A kind and action hub does not relay are
// ignored. For one it relays, the repository the payload names must be the workspace's origin or,
// on a carry_patch workspace, its upstream, else the delivery is for another repository and
// nothing is relayed. A delivery of any kind whose repository is the workspace's is bound to it.
func reduceDelivery(d delivery, ws workspaceRemotes) reduction {
	doc, err := parsePayload(d.body)
	if err != nil {
		return reduction{outcome: outcomeMalformed}
	}
	red := reduction{action: actionOf(d.forge, doc)}
	path, havePath := repositoryOf(d.forge, doc)
	if havePath {
		red.target = bindRepository(path, ws)
	}

	m := mapperFor(d.forge, d.event, red.action)
	switch {
	case m == nil:
		red.outcome = outcomeIgnored
		return red
	case !havePath:
		red.outcome, red.target = outcomeMalformed, ""
		return red
	case red.target == "":
		red.outcome = outcomeMismatch
		return red
	}
	events, err := m(&builder{d: d, doc: doc, tgt: red.target})
	switch {
	case err != nil:
		red.outcome, red.target = outcomeMalformed, ""
	case len(events) == 0:
		red.outcome = outcomeIgnored
	default:
		red.outcome, red.events = outcomeRelayed, events
	}
	return red
}

// mapperFor returns the mapper of an event and action of a forge, or nil for one hub does not
// relay.
func mapperFor(forge, event, action string) mapper {
	if forge == forgeGitHub {
		return githubMapper(event, action)
	}
	return nil
}

// actionOf reads the action of a payload; "" when it has none.
func actionOf(forge string, doc payload) string {
	if forge == forgeGitHub {
		action, _ := doc.str("action")
		return action
	}
	action, _ := doc.str("object_attributes", "action")
	return action
}

// repositoryOf reads the path of the repository a payload is about.
func repositoryOf(forge string, doc payload) (string, bool) {
	if forge == forgeGitHub {
		return doc.str("repository", "full_name")
	}
	return doc.str("project", "path_with_namespace")
}

// bindRepository tells which of the workspace's repositories a path names, ignoring case: the
// origin, else the upstream of a carry_patch workspace, else none. A remote that cannot be read
// names none.
func bindRepository(path string, ws workspaceRemotes) target {
	switch {
	case remoteIs(ws.gitURL, path):
		return targetOrigin
	case ws.hasUpstream() && remoteIs(ws.upstreamURL, path):
		return targetUpstream
	}
	return ""
}

// remoteIs reports whether a remote URL names the repository at path, owner path and name.
func remoteIs(remote, path string) bool {
	repo, ok := parseRemote(remote)
	return ok && strings.EqualFold(repo.Owner+"/"+repo.Name, path)
}

// builder makes the events of one delivery.
type builder struct {
	d   delivery
	doc payload
	tgt target
	// actor is the login the forge gives the delivery, set by the mapper of the forge.
	actor string
}

// event makes the hub event of the delivery. The actor is the forge, not a hub identity, and
// the metadata always says which forge, which repository, which delivery and who acted.
func (b *builder) event(eventType, resourceType, resourceID string, metadata map[string]any) audit.HubEvent {
	metadata["forge"] = b.d.forge
	metadata["target"] = string(b.tgt)
	metadata["delivery_id"] = b.d.id
	metadata["actor"] = b.actor
	return audit.HubEvent{
		EventType:    eventType,
		ActorType:    "forge",
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Action:       eventType[strings.LastIndexByte(eventType, '.')+1:],
		Workspace:    b.d.slug,
		Metadata:     metadata,
	}
}

// payload is the JSON object of a delivery. Numbers are json.Number, so that a number the
// forge sends is not rounded.
type payload map[string]any

// parsePayload reads a body as one JSON object and nothing after it.
func parsePayload(data []byte) (payload, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc payload
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errMalformed
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errMalformed
	}
	return doc, nil
}

// at returns the value at the path of keys, or nil when any step is not an object that has it.
func (p payload) at(keys ...string) any {
	var cur any = map[string]any(p)
	for _, key := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[key]
	}
	return cur
}

// str returns the string at the path; false when there is none.
func (p payload) str(keys ...string) (string, bool) {
	s, ok := p.at(keys...).(string)
	return s, ok
}

// integer returns the whole number above zero at the path; false when there is none.
func (p payload) integer(keys ...string) (int, bool) {
	n, ok := p.at(keys...).(json.Number)
	if !ok {
		return 0, false
	}
	v, err := n.Int64()
	if err != nil || v < 1 || v != int64(int(v)) {
		return 0, false
	}
	return int(v), true
}

// names returns the string under key of every object of the array at the path, in order, as a
// list that is empty and not nil when there is none, so that it encodes as [].
func (p payload) names(key string, keys ...string) []string {
	out := []string{}
	items, _ := p.at(keys...).([]any)
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			if name, ok := obj[key].(string); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// maxTitleBytes is the longest title an event holds.
const maxTitleBytes = 256

// title cuts a title from a payload to maxTitleBytes on a UTF-8 boundary.
func title(s string) string {
	return truncateUTF8(strings.ToValidUTF8(s, "\uFFFD"), maxTitleBytes)
}

// payloadURL is a URL from a payload without its user information, which the audit log must
// not hold.
func payloadURL(s string) string {
	return userinfoPattern.ReplaceAllString(s, "${1}")
}

// need turns a missing field into the error of a payload that lacks it.
func need(ok bool, what string) error {
	if !ok {
		return fmt.Errorf("%w: %s", errMalformed, what)
	}
	return nil
}
