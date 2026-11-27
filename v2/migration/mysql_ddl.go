package migration

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MySQL emits versioned comments for options such as invisible indexes. Keep
// those tokens, while dropping ordinary comments and SQL formatting.
var (
	mysqlDDLTokens          = regexp.MustCompile(`(?s)/\*![0-9]+.*?\*/|/\*.*?\*/|'(?:''|\\.|[^'\\])*'|` + "`(?:``|[^`])*`" + `|[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?|[A-Za-z_$][A-Za-z0-9_$]*|\S`)
	mysqlDDLTokensNoEscapes = regexp.MustCompile(strings.Replace(mysqlDDLTokens.String(), `'(?:''|\\.|[^'\\])*'`, `'(?:''|[^'])*'`, 1))
	mysqlDecimal            = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)
)

func ddlTokens(sql string, noEscapes bool) []string {
	pattern := mysqlDDLTokens
	if noEscapes {
		pattern = mysqlDDLTokensNoEscapes
	}
	var tokens []string
	for _, token := range pattern.FindAllString(sql, -1) {
		if strings.HasPrefix(token, "/*") && !strings.HasPrefix(token, "/*!") {
			continue
		}
		if !strings.HasPrefix(token, "'") && !strings.HasPrefix(token, "`") && !strings.HasPrefix(token, "/*!") {
			token = strings.ToLower(token)
		}
		tokens = append(tokens, token)
	}
	return tokens
}

type mysqlDDL struct {
	columns [][]string
	keys    [][]string
	options []string
}

func parseMySQLDDL(sql string, noEscapes bool) (mysqlDDL, error) {
	tokens := ddlTokens(sql, noEscapes)
	start := slices.Index(tokens, "(")
	if len(tokens) < 4 || tokens[0] != "create" || tokens[1] != "table" || start < 3 {
		return mysqlDDL{}, fmt.Errorf("expected one CREATE TABLE definition")
	}
	end := ddlCloseParen(tokens, start)
	if end < 0 {
		return mysqlDDL{}, fmt.Errorf("unbalanced CREATE TABLE definition")
	}
	result := mysqlDDL{options: tokens[end+1:]}
	for _, term := range ddlTerms(tokens[start+1 : end]) {
		if len(term) < 2 {
			return mysqlDDL{}, fmt.Errorf("incomplete CREATE TABLE definition")
		}
		switch term[0] {
		case "primary", "unique", "index", "key", "fulltext", "spatial", "foreign", "check", "constraint":
			result.keys = append(result.keys, term)
		default:
			result.columns = append(result.columns, term)
		}
	}
	return result, nil
}

func ddlCloseParen(tokens []string, start int) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		switch tokens[i] {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func ddlTerms(tokens []string) [][]string {
	var terms [][]string
	depth, start := 0, 0
	for i, token := range tokens {
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				terms = append(terms, tokens[start:i])
				start = i + 1
			}
		}
	}
	return append(terms, tokens[start:])
}

func ddlName(token string) string {
	if len(token) >= 2 && token[0] == 96 {
		return strings.ReplaceAll(token[1:len(token)-1], "``", "`")
	}
	return token
}

func (d mysqlDDL) primaryColumn(name string) bool {
	for _, key := range d.keys {
		if len(key) < 4 || key[0] != "primary" || key[1] != "key" {
			continue
		}
		end := ddlCloseParen(key, 2)
		for _, field := range key[3:max(3, end)] {
			if strings.EqualFold(ddlName(field), name) {
				return true
			}
		}
	}
	return false
}

func ddlLiteral(token string, noEscapes bool) string {
	if len(token) < 2 || token[0] != '\'' || token[len(token)-1] != '\'' {
		return token
	}
	value := token[1 : len(token)-1]
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\'' && i+1 < len(value) && value[i+1] == '\'' {
			i++
		} else if c == '\\' && !noEscapes && i+1 < len(value) {
			i++
			c = value[i]
			switch c {
			case '0':
				c = 0
			case 'b':
				c = '\b'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'Z':
				c = 26
			case '%', '_':
				out.WriteByte('\\')
			}
		}
		out.WriteByte(c)
	}
	return "'" + strconv.Quote(out.String())
}

func canonicalColumn(tokens []string, primary, noEscapes bool) []string {
	if len(tokens) == 0 {
		return nil
	}
	typeName := tokens[0]
	if typeName == "bool" || typeName == "boolean" {
		tokens = append([]string{"tinyint", "(", "1", ")"}, tokens[1:]...)
		typeName = "tinyint"
	}
	switch typeName {
	case "integer":
		typeName = "int"
	case "decimal", "dec", "numeric", "fixed":
		typeName = "decimal"
	}
	isInteger := slices.Contains([]string{"tinyint", "smallint", "mediumint", "int", "bigint"}, typeName)
	isNumeric := isInteger || slices.Contains([]string{"decimal", "float", "double"}, typeName)
	out := []string{typeName}
	start := 1
	// MySQL 8 omits integer display widths, except for tinyint(1) and ZEROFILL.
	if len(tokens) > 3 && tokens[1] == "(" && tokens[3] == ")" {
		ignoreWidth := isInteger && !slices.Contains(tokens, "zerofill") && (typeName != "tinyint" || tokens[2] != "1")
		zeroPrecision := (typeName == "datetime" || typeName == "timestamp" || typeName == "time") && tokens[2] == "0"
		if ignoreWidth || zeroPrecision {
			start = 4
		}
	}
	depth := 0
	for i := start; i < len(tokens); i++ {
		token := tokens[i]
		if depth == 0 {
			switch {
			case token == "not" && i+1 < len(tokens) && tokens[i+1] == "null":
				if !primary {
					out = append(out, "not", "null")
				}
				i++
				continue
			case token == "null":
				continue
			case token == "signed" && isNumeric:
				continue
			case token == "default" && i+1 < len(tokens):
				value := tokens[i+1]
				if value == "null" && !primary && !slices.Contains(tokens, "not") {
					i++
					continue
				}
				if isNumeric {
					if n, ok := canonicalNumericDefault(value, noEscapes); ok {
						out = append(out, "default", n)
						i++
						continue
					}
				}
			case token == "current_timestamp" && i+2 < len(tokens) && tokens[i+1] == "(":
				if tokens[i+2] == ")" {
					i += 2
				} else if i+3 < len(tokens) && tokens[i+2] == "0" && tokens[i+3] == ")" {
					i += 3
				}
			}
		}
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		}
		out = append(out, ddlLiteral(token, noEscapes))
	}
	return out
}

func canonicalNumericDefault(token string, noEscapes bool) (string, bool) {
	switch token {
	case "true":
		return "number:1", true
	case "false":
		return "number:0", true
	}
	value := token
	if atom := ddlLiteral(token, noEscapes); strings.HasPrefix(atom, "'\"") {
		decoded, err := strconv.Unquote(atom[1:])
		if err != nil {
			return "", false
		}
		value = decoded
	}
	if !mysqlDecimal.MatchString(value) {
		return "", false
	}
	negative := strings.HasPrefix(value, "-")
	whole, fraction, _ := strings.Cut(strings.TrimLeft(value, "+-"), ".")
	whole, fraction = strings.TrimLeft(whole, "0"), strings.TrimRight(fraction, "0")
	if whole == "" {
		whole = "0"
	}
	if fraction != "" {
		whole += "." + fraction
	}
	if negative && whole != "0" {
		whole = "-" + whole
	}
	return "number:" + whole, true
}

func canonicalKey(tokens []string, noEscapes bool) string {
	var key []string
	depth := 0
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if (i == 0 || i == 1 && slices.Contains([]string{"unique", "fulltext", "spatial"}, tokens[0])) && token == "index" {
			token = "key"
		}
		if depth == 0 && token == "using" && i+1 < len(tokens) && tokens[i+1] == "btree" {
			i++
			continue
		}
		if depth == 1 && token == "asc" && i+1 < len(tokens) && (tokens[i+1] == "," || tokens[i+1] == ")") {
			continue
		}
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		}
		key = append(key, ddlLiteral(token, noEscapes))
	}
	return strings.Join(key, "\x00")
}

func canonicalKeys(def mysqlDDL, noEscapes bool) []string {
	keys := make([]string, 0, len(def.keys))
	for _, key := range def.keys {
		keys = append(keys, canonicalKey(canonicalCheck(key, def.columns), noEscapes))
	}
	slices.Sort(keys)
	return keys
}

func canonicalCheck(key []string, columns [][]string) []string {
	check := 0
	if key[0] == "constraint" {
		check = 2
	}
	if len(key) <= check+2 || key[check] != "check" || key[check+1] != "(" {
		return key
	}
	end := ddlCloseParen(key, check+1)
	if end < 0 {
		return key
	}
	expression := slices.Clone(key[check+2 : end])
	for len(expression) > 2 && expression[0] == "(" && ddlCloseParen(expression, 0) == len(expression)-1 {
		expression = expression[1 : len(expression)-1]
	}
	// SHOW CREATE quotes simple column references in CHECK predicates.
	for i, token := range expression {
		if i+1 == len(expression) || !slices.Contains([]string{"=", "<", ">", "!"}, expression[i+1]) {
			continue
		}
		name := ddlName(token)
		switch strings.ToLower(name) {
		case "true", "false", "null", "unknown", "current_date", "current_time", "current_timestamp", "localtime", "localtimestamp":
			continue
		}
		for _, column := range columns {
			if strings.EqualFold(name, ddlName(column[0])) {
				expression[i] = "`" + strings.ToLower(name) + "`"
				break
			}
		}
	}
	normalized := append(slices.Clone(key[:check+2]), expression...)
	return append(normalized, key[end:]...)
}

type mysqlDDLOptions struct {
	engine, charset, collation string
	noEscapes                  bool
}

func canonicalTableOptions(tokens []string, defaults mysqlDDLOptions) map[string]string {
	out := map[string]string{"engine": defaults.engine, "charset": defaults.charset, "collate": defaults.collation}
	remainder := make([]string, 0)
	for i := 0; i < len(tokens); i++ {
		key := tokens[i]
		if key == "default" && i+1 < len(tokens) && slices.Contains([]string{"charset", "character", "collate"}, tokens[i+1]) {
			continue
		}
		if key == "character" && i+1 < len(tokens) && tokens[i+1] == "set" {
			key = "charset"
			i++
		}
		if i+2 < len(tokens) && tokens[i+1] == "=" &&
			slices.Contains([]string{"engine", "charset", "collate", "auto_increment"}, key) {
			value := ddlLiteral(tokens[i+2], defaults.noEscapes)
			if key != "auto_increment" {
				out[key] = value
			}
			i += 2
		} else if key != ";" || i != len(tokens)-1 {
			remainder = append(remainder, ddlLiteral(key, defaults.noEscapes))
		}
	}
	if len(remainder) != 0 {
		out["remaining"] = strings.Join(remainder, "\x00")
	}
	return out
}
