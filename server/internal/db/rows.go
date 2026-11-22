package db

import (
	"reflect"
	"strconv"
	"strings"
)

// A row of a wide table is written as a struct whose fields carry the
// column each holds (`col:"case_number"`), in the table's order of
// writing. The column list of a statement and the values or the places
// to scan into come from the same struct, so they cannot fall out of
// step. A field with no col tag is not a column.

func rowType(row any) reflect.Type {
	t := reflect.TypeOf(row)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// Columns are the columns a row struct names, in order.
func Columns(row any) []string {
	t := rowType(row)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		if c := t.Field(i).Tag.Get("col"); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Fields are pointers to the fields of *row that are columns, in the
// order of Columns: what a query scans into, and what a statement is
// given as its values.
func Fields(row any) []any {
	v := reflect.ValueOf(row).Elem()
	t := v.Type()
	var out []any
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("col") != "" {
			out = append(out, v.Field(i).Addr().Interface())
		}
	}
	return out
}

// SelectSQL reads every column of a row struct: "SELECT a, b FROM table"
// and whatever follows.
func SelectSQL(table string, row any, rest string) string {
	return "SELECT " + strings.Join(Columns(row), ", ") + " FROM " + table + " " + rest
}

// InsertSQL writes every column of a row struct, the values numbered
// from $1 in the order of Fields.
func InsertSQL(table string, row any) string {
	cols := Columns(row)
	marks := make([]string, len(cols))
	for i := range cols {
		marks[i] = "$" + strconv.Itoa(i+1)
	}
	return "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
}

// UpdateSQL sets every column of a row struct, the values numbered from
// $1 in the order of Fields; where is given the numbers after them.
func UpdateSQL(table string, row any, where string) string {
	cols := Columns(row)
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = c + " = $" + strconv.Itoa(i+1)
	}
	return "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
}
