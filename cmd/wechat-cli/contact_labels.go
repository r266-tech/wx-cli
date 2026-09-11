package main

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

// WeChat 4.x ContactInfo stores a comma-delimited list of contact label IDs in
// top-level protobuf field 30. Names and display order live in contact_label.
const contactLabelListProtoField = uint64(30)

type contactLabelDefinition struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	SortOrder    int64  `json:"sort_order"`
	ContactCount int64  `json:"contact_count"`
}

type contactLabelRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`
}

func (s *server) toolContactLabels(a map[string]any) (any, error) {
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	labels, err := loadContactLabelDefinitions(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT extra_buffer FROM contact
		WHERE extra_buffer IS NOT NULL AND length(extra_buffer) > 0`)
	if err != nil {
		return nil, fmt.Errorf("count contact labels: %w", err)
	}
	counts := countContactLabelMemberships(rows)

	keyword := normalizeContactLabelSearch(getStr(a, "keyword"))
	filtered := make([]contactLabelDefinition, 0, len(labels))
	for _, label := range labels {
		if keyword != "" && !strings.Contains(normalizeContactLabelSearch(label.Name), keyword) {
			continue
		}
		label.ContactCount = counts[label.ID]
		filtered = append(filtered, label)
	}

	offset := maxInt(getInt(a, "offset", 0), 0)
	limit := getInt(a, "limit", 50)
	if offset >= len(filtered) || limit == 0 {
		return []wcdb.Row{}, nil
	}
	end := len(filtered)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := make([]wcdb.Row, 0, end-offset)
	for _, label := range filtered[offset:end] {
		out = append(out, wcdb.Row{
			"id":            label.ID,
			"name":          label.Name,
			"sort_order":    label.SortOrder,
			"contact_count": label.ContactCount,
		})
	}
	return out, nil
}

func loadContactLabelDefinitions(db *wcdb.DB) ([]contactLabelDefinition, error) {
	rows, err := db.Query(`SELECT label_id_ AS label_id, label_name_ AS label_name,
		COALESCE(sort_order_, 0) AS sort_order
		FROM contact_label
		WHERE label_id_ > 0
		ORDER BY sort_order_, label_id_`)
	if err != nil {
		return nil, fmt.Errorf("read contact labels: %w", err)
	}
	labels := make([]contactLabelDefinition, 0, len(rows))
	for _, row := range rows {
		id := rowInt64(row, "label_id")
		if id <= 0 {
			continue
		}
		labels = append(labels, contactLabelDefinition{
			ID:        id,
			Name:      rowString(row, "label_name"),
			SortOrder: rowInt64(row, "sort_order"),
		})
	}
	return labels, nil
}

func contactLabelDefinitionsByID(labels []contactLabelDefinition) map[int64]contactLabelDefinition {
	out := make(map[int64]contactLabelDefinition, len(labels))
	for _, label := range labels {
		out[label.ID] = label
	}
	return out
}

func countContactLabelMemberships(rows []wcdb.Row) map[int64]int64 {
	counts := map[int64]int64{}
	for _, row := range rows {
		for _, id := range contactLabelIDs(rowBytes(row, "extra_buffer")) {
			counts[id]++
		}
	}
	return counts
}

func contactLabelRefs(extraBuffer []byte, definitions map[int64]contactLabelDefinition) []contactLabelRef {
	ids := contactLabelIDs(extraBuffer)
	refs := make([]contactLabelRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, contactLabelRef{ID: id, Name: definitions[id].Name})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		left, leftKnown := definitions[refs[i].ID]
		right, rightKnown := definitions[refs[j].ID]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && left.SortOrder != right.SortOrder {
			return left.SortOrder < right.SortOrder
		}
		return refs[i].ID < refs[j].ID
	})
	return refs
}

func contactLabelIDs(extraBuffer []byte) []int64 {
	values := protoTopLevelLengthDelimitedFields(extraBuffer, contactLabelListProtoField)
	ids := make([]int64, 0)
	seen := map[int64]bool{}
	for _, value := range values {
		parts := strings.FieldsFunc(strings.TrimSpace(string(value)), func(r rune) bool {
			switch r {
			case ',', '，', ';', '；', '|':
				return true
			}
			return r == ' ' || r == '\t' || r == '\r' || r == '\n'
		})
		for _, part := range parts {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func protoTopLevelLengthDelimitedFields(data []byte, targetField uint64) [][]byte {
	if len(data) == 0 || targetField == 0 {
		return nil
	}
	values := make([][]byte, 0, 1)
	for offset := 0; offset < len(data); {
		key, n := binary.Uvarint(data[offset:])
		if n <= 0 || key == 0 {
			break
		}
		offset += n
		fieldNumber := key >> 3
		wireType := key & 0x07
		switch wireType {
		case 0:
			_, n = binary.Uvarint(data[offset:])
			if n <= 0 {
				return values
			}
			offset += n
		case 1:
			if len(data)-offset < 8 {
				return values
			}
			offset += 8
		case 2:
			length, lengthBytes := binary.Uvarint(data[offset:])
			if lengthBytes <= 0 {
				return values
			}
			offset += lengthBytes
			if length > uint64(len(data)-offset) {
				return values
			}
			end := offset + int(length)
			if fieldNumber == targetField {
				values = append(values, data[offset:end])
			}
			offset = end
		case 5:
			if len(data)-offset < 4 {
				return values
			}
			offset += 4
		default:
			// Groups and unknown wire types are not used by WeChat's ContactInfo
			// protobuf. Stop rather than guessing a skip length.
			return values
		}
	}
	return values
}

func contactLabelFilterIDs(labels []contactLabelDefinition, a map[string]any) (map[int64]bool, bool) {
	name := strings.TrimSpace(getStr(a, "label"))
	_, idProvided := a["label_id"]
	id := int64(getInt(a, "label_id", 0))
	active := name != "" || idProvided
	if !active {
		return nil, false
	}

	nameMatches := map[int64]bool{}
	if name != "" {
		normalized := normalizeContactLabelSearch(name)
		for _, label := range labels {
			if normalizeContactLabelSearch(label.Name) == normalized {
				nameMatches[label.ID] = true
			}
		}
		if len(nameMatches) == 0 {
			for _, label := range labels {
				if strings.Contains(normalizeContactLabelSearch(label.Name), normalized) {
					nameMatches[label.ID] = true
				}
			}
		}
	}

	if idProvided {
		if name == "" || nameMatches[id] {
			return map[int64]bool{id: true}, true
		}
		return map[int64]bool{}, true
	}
	return nameMatches, true
}

func contactHasAnyLabel(extraBuffer []byte, wanted map[int64]bool) bool {
	for _, id := range contactLabelIDs(extraBuffer) {
		if wanted[id] {
			return true
		}
	}
	return false
}

func normalizeContactLabelSearch(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), ""))
}
