package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

// ============================================================================
// 1. LEXER & TOKEN DEFINITIONS
// ============================================================================

type TokenType int

const (
	TokenEOF TokenType = iota
	TokenError
	TokenIdentifier
	TokenOperator     // =, +=
	TokenDot          // .
	TokenOpenParen    // (
	TokenCloseParen   // )
	TokenOpenBracket  // [
	TokenCloseBracket // ]
	TokenComma
	TokenStringLiteral
	TokenNumber
	TokenBashLine   // Standard passthrough Bash line
	TokenKeywordFor // for
	TokenKeywordIn  // in
	TokenColon      // :
	TokenRawBlock   // __raw__:
	TokenEndBlock   // __end__
	TokenDone       // done (explicit keyword, not identifier)
)

type Token struct {
	Type   TokenType
	Value  string
	Line   int
	Column int
}

type Lexer struct {
	input string
	pos   int
	line  int
	col   int

	// lineStart/lineClassDone/lineIsForge cache the classification decision
	// for the line currently being scanned, keyed by the byte offset where
	// that line begins. Classification must happen once against the whole
	// line, not be re-evaluated against the shrinking remainder after each
	// token is consumed -- by then the leading identifier the classifier
	// depends on (e.g. "servers" in "servers = list()") is already gone.
	lineStart     int
	lineClassDone bool
	lineIsForge   bool
}

func NewLexer(input string) *Lexer {
	return &Lexer{input: input, pos: 0, line: 1, col: 1, lineStart: -1}
}

func (l *Lexer) NextToken() Token {
	// Consume whitespace and newlines iteratively (no recursion).
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' {
			l.pos++
			l.col++
			continue
		}
		if ch == '\n' {
			l.line++
			l.col = 1
			l.pos++
			continue
		}
		break
	}

	if l.pos >= len(l.input) {
		return Token{Type: TokenEOF, Value: "", Line: l.line, Column: l.col}
	}

	startLine, startCol := l.line, l.col

	if strings.HasPrefix(l.input[l.pos:], "__raw__:") {
		l.advance(len("__raw__:"))
		return Token{Type: TokenRawBlock, Value: "__raw__:", Line: startLine, Column: startCol}
	}
	if strings.HasPrefix(l.input[l.pos:], "__end__") {
		l.advance(len("__end__"))
		return Token{Type: TokenEndBlock, Value: "__end__", Line: startLine, Column: startCol}
	}

	// A bare 'done' line closes a for-loop body. Checked before line
	// classification so it is never swallowed as a generic Bash line.
	if l.lineIsBareKeyword("done") {
		l.advance(len("done"))
		return Token{Type: TokenDone, Value: "done", Line: startLine, Column: startCol}
	}

	// Decide whether the rest of this line is BashForge syntax or opaque
	// Bash. Classification is computed once per physical line (cached),
	// since re-deriving it from the shrinking remainder after each token is
	// consumed loses the leading identifier the classifier depends on.
	if l.lineForgeStatus() {
		ch := l.input[l.pos]
		switch ch {
		case '.':
			l.advance(1)
			return Token{Type: TokenDot, Value: ".", Line: startLine, Column: startCol}
		case '(':
			l.advance(1)
			return Token{Type: TokenOpenParen, Value: "(", Line: startLine, Column: startCol}
		case ')':
			l.advance(1)
			return Token{Type: TokenCloseParen, Value: ")", Line: startLine, Column: startCol}
		case '[':
			l.advance(1)
			return Token{Type: TokenOpenBracket, Value: "[", Line: startLine, Column: startCol}
		case ']':
			l.advance(1)
			return Token{Type: TokenCloseBracket, Value: "]", Line: startLine, Column: startCol}
		case ',':
			l.advance(1)
			return Token{Type: TokenComma, Value: ",", Line: startLine, Column: startCol}
		case ':':
			l.advance(1)
			return Token{Type: TokenColon, Value: ":", Line: startLine, Column: startCol}
		case '=':
			l.advance(1)
			return Token{Type: TokenOperator, Value: "=", Line: startLine, Column: startCol}
		case '"', '\'':
			return l.readStringLiteral(ch, startLine, startCol)
		}

		if isIdentifierStart(ch) {
			ident := l.readIdentifier()
			switch ident {
			case "for":
				return Token{Type: TokenKeywordFor, Value: "for", Line: startLine, Column: startCol}
			case "in":
				return Token{Type: TokenKeywordIn, Value: "in", Line: startLine, Column: startCol}
			}
			return Token{Type: TokenIdentifier, Value: ident, Line: startLine, Column: startCol}
		}

		if unicode.IsDigit(rune(ch)) {
			return Token{Type: TokenNumber, Value: l.readNumber(), Line: startLine, Column: startCol}
		}
	}

	// Not BashForge syntax: pass the entire remaining line through untouched.
	return l.readBashLine(startLine, startCol)
}

// lineIsBareKeyword reports whether the current position starts a line
// that consists of exactly the given keyword (optionally followed only by
// whitespace and then a newline or EOF).
func (l *Lexer) lineIsBareKeyword(kw string) bool {
	rest := l.input[l.pos:]
	if !strings.HasPrefix(rest, kw) {
		return false
	}
	after := rest[len(kw):]
	for _, ch := range after {
		if ch == '\n' {
			return true
		}
		if ch != ' ' && ch != '\t' && ch != '\r' {
			return false
		}
	}
	return true // EOF after keyword
}

// lineForgeStatus returns whether the physical line containing l.pos is
// BashForge syntax or opaque Bash, computing it once per line and caching
// the result keyed by the line's starting byte offset.
func (l *Lexer) lineForgeStatus() bool {
	start := l.currentLineStart()
	if l.lineClassDone && l.lineStart == start {
		return l.lineIsForge
	}
	end := strings.IndexByte(l.input[start:], '\n')
	var line string
	if end == -1 {
		line = l.input[start:]
	} else {
		line = l.input[start : start+end]
	}
	trimmed := strings.TrimSpace(line)

	isForge := false
	switch {
	case trimmed == "":
		isForge = false
	case strings.HasPrefix(trimmed, "for ") && strings.Contains(trimmed, " in ") && strings.HasSuffix(trimmed, ":"):
		isForge = true
	case isAssignmentToCollectionLiteral(trimmed):
		isForge = true
	case isMethodCallLine(trimmed):
		isForge = true
	case isDictIndexAssignLine(trimmed):
		isForge = true
	}

	l.lineStart = start
	l.lineClassDone = true
	l.lineIsForge = isForge
	return isForge
}

// currentLineStart returns the byte offset of the start of the line
// containing l.pos.
func (l *Lexer) currentLineStart() int {
	idx := strings.LastIndexByte(l.input[:l.pos], '\n')
	return idx + 1
}

func isAssignmentToCollectionLiteral(line string) bool {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return false
	}
	name := strings.TrimSpace(parts[0])
	rhs := strings.TrimSpace(parts[1])
	if !isValidIdentifierToken(name) {
		return false
	}
	for _, ctor := range []string{"list()", "dict()", "set()"} {
		if rhs == ctor {
			return true
		}
	}
	return false
}

func isMethodCallLine(line string) bool {
	dot := strings.IndexByte(line, '.')
	paren := strings.IndexByte(line, '(')
	if dot == -1 || paren == -1 || paren < dot {
		return false
	}
	name := line[:dot]
	if !isValidIdentifierToken(name) {
		return false
	}
	return strings.HasSuffix(line, ")")
}

func isDictIndexAssignLine(line string) bool {
	bracket := strings.IndexByte(line, '[')
	if bracket == -1 {
		return false
	}
	name := line[:bracket]
	if !isValidIdentifierToken(name) {
		return false
	}
	return strings.Contains(line, "]") && strings.Contains(line, "=")
}

func isValidIdentifierToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if i == 0 {
			if !isIdentifierStart(ch) {
				return false
			}
		} else if !isIdentifierPart(ch) {
			return false
		}
	}
	return true
}

// advance moves pos forward by n bytes, keeping column tracking in sync.
// Assumes the advanced-over bytes do not contain newlines (true for all callers).
func (l *Lexer) advance(n int) {
	l.pos += n
	l.col += n
}

func (l *Lexer) readIdentifier() string {
	start := l.pos
	for l.pos < len(l.input) && (isIdentifierPart(l.input[l.pos])) {
		l.pos++
		l.col++
	}
	return l.input[start:l.pos]
}

func (l *Lexer) readNumber() string {
	start := l.pos
	for l.pos < len(l.input) && unicode.IsDigit(rune(l.input[l.pos])) {
		l.pos++
		l.col++
	}
	return l.input[start:l.pos]
}

func (l *Lexer) readStringLiteral(quote byte, startLine, startCol int) Token {
	l.advance(1) // consume starting quote
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != quote {
		if l.input[l.pos] == '\n' {
			l.line++
			l.col = 1
			l.pos++
		} else {
			l.pos++
			l.col++
		}
	}
	val := l.input[start:l.pos]
	if l.pos < len(l.input) {
		l.advance(1) // consume ending quote
	}
	return Token{Type: TokenStringLiteral, Value: val, Line: startLine, Column: startCol}
}

func (l *Lexer) readBashLine(startLine, startCol int) Token {
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != '\n' {
		l.pos++
		l.col++
	}
	val := l.input[start:l.pos]
	return Token{Type: TokenBashLine, Value: val, Line: startLine, Column: startCol}
}

// isIdentifierStart: letters and underscore only. '-' and '/' and '$' removed
// because they caused paths, flags, and Bash variable expansions to be
// mis-lexed as identifiers.
func isIdentifierStart(ch byte) bool {
	return unicode.IsLetter(rune(ch)) || ch == '_'
}

func isIdentifierPart(ch byte) bool {
	return unicode.IsLetter(rune(ch)) || unicode.IsDigit(rune(ch)) || ch == '_'
}

// ============================================================================
// 2. ABSTRACT SYNTAX TREE (AST) & PARSER TYPES
// ============================================================================

type CollectionType string

const (
	TypeUnknown CollectionType = "unknown"
	TypeList    CollectionType = "list"
	TypeDict    CollectionType = "dict"
	TypeSet     CollectionType = "set"
)

type Position struct {
	Line   int
	Column int
}

type ASTNode interface {
	sealed()
	Pos() Position
}

type Program struct {
	Nodes []ASTNode
}

type BashPassthroughNode struct {
	Code     string
	Position Position
}

type RawBlockNode struct {
	InnerContent string
	Position     Position
}

type CollectionInitNode struct {
	VarName  string
	Type     CollectionType
	Position Position
}

type MethodCallNode struct {
	VarName  string
	Method   string
	Args     []string
	Position Position
}

type DictAssignNode struct {
	VarName  string
	Key      string
	Value    string
	Position Position
}

type ForLoopNode struct {
	LoopVars   []string
	IterTarget string
	Body       []ASTNode
	Position   Position
}

func (p *Program) sealed()              {}
func (b BashPassthroughNode) sealed()   {}
func (r RawBlockNode) sealed()          {}
func (c CollectionInitNode) sealed()    {}
func (m MethodCallNode) sealed()        {}
func (d DictAssignNode) sealed()        {}
func (f ForLoopNode) sealed()           {}

func (p *Program) Pos() Position            { return Position{} }
func (b BashPassthroughNode) Pos() Position { return b.Position }
func (r RawBlockNode) Pos() Position        { return r.Position }
func (c CollectionInitNode) Pos() Position  { return c.Position }
func (m MethodCallNode) Pos() Position      { return m.Position }
func (d DictAssignNode) Pos() Position      { return d.Position }
func (f ForLoopNode) Pos() Position         { return f.Position }

// ============================================================================
// 3. PARSER ENGINE
// ============================================================================

type ParseError struct {
	Line    int
	Column  int
	Message string
}

func (e *ParseError) String() string {
	return fmt.Sprintf("line %d, col %d: %s", e.Line, e.Column, e.Message)
}

type Parser struct {
	lexer     *Lexer
	currToken Token
	peekToken Token
	symTable  map[string]CollectionType
	errors    []*ParseError
}

func NewParser(lexer *Lexer) *Parser {
	p := &Parser{
		lexer:    lexer,
		symTable: make(map[string]CollectionType),
	}
	p.nextToken()
	p.nextToken()
	return p
}

func (p *Parser) Errors() []*ParseError {
	return p.errors
}

func (p *Parser) addError(msg string) {
	p.errors = append(p.errors, &ParseError{
		Line:    p.currToken.Line,
		Column:  p.currToken.Column,
		Message: msg,
	})
}

func (p *Parser) nextToken() {
	p.currToken = p.peekToken
	p.peekToken = p.lexer.NextToken()
}

func (p *Parser) Parse() *Program {
	program := &Program{Nodes: []ASTNode{}}

	for p.currToken.Type != TokenEOF {
		node := p.parseStatement()
		if node != nil {
			program.Nodes = append(program.Nodes, node)
		}
		p.nextToken()
	}
	return program
}

func (p *Parser) parseStatement() ASTNode {
	switch p.currToken.Type {
	case TokenRawBlock:
		return p.parseRawBlock()

	case TokenKeywordFor:
		return p.parseForLoop()

	case TokenIdentifier:
		// Check for method dispatch sequence: identity.method(...)
		if p.peekToken.Type == TokenDot {
			return p.parseMethodCall()
		}
		// Check for collection index assignment: dict["key"] = val
		if p.peekToken.Type == TokenOpenBracket {
			return p.parseDictAssignment()
		}
		// Check for standard declaration assignment: identity = collection()
		if p.peekToken.Type == TokenOperator && p.peekToken.Value == "=" {
			return p.parseAssignmentOrInit()
		}
		return p.parseBashPassthroughSingle()

	case TokenBashLine:
		return p.parseBashPassthroughSingle()

	case TokenDone:
		// A stray 'done' outside a for-loop body is passed through as-is;
		// it is not consumed specially here.
		return p.parseBashPassthroughSingle()
	}
	return nil
}

// parseBashPassthroughSingle consumes exactly the current token as opaque
// Bash content. It does NOT swallow the next token, which was the source of
// a class of corruption bugs in the previous version.
func (p *Parser) parseBashPassthroughSingle() ASTNode {
	pos := Position{Line: p.currToken.Line, Column: p.currToken.Column}
	val := strings.TrimSpace(p.currToken.Value)
	return BashPassthroughNode{Code: val, Position: pos}
}

func (p *Parser) parseRawBlock() ASTNode {
	pos := Position{Line: p.currToken.Line, Column: p.currToken.Column}
	var sb strings.Builder
	p.nextToken() // consume __raw__: marker

	for p.currToken.Type != TokenEndBlock && p.currToken.Type != TokenEOF {
		sb.WriteString(p.currToken.Value + "\n")
		p.nextToken()
	}
	if p.currToken.Type != TokenEndBlock {
		p.addError("unterminated __raw__ block, expected __end__")
	}
	return RawBlockNode{InnerContent: sb.String(), Position: pos}
}

func (p *Parser) parseAssignmentOrInit() ASTNode {
	pos := Position{Line: p.currToken.Line, Column: p.currToken.Column}
	varName := p.currToken.Value
	p.nextToken() // pass varName
	p.nextToken() // pass '='

	if p.currToken.Type == TokenIdentifier {
		switch p.currToken.Value {
		case "list", "dict", "set":
			cType := CollectionType(p.currToken.Value)
			p.symTable[varName] = cType
			p.nextToken() // consume constructor name
			if p.currToken.Type == TokenOpenParen {
				p.nextToken() // consume '('
				if p.currToken.Type != TokenCloseParen {
					p.addError(fmt.Sprintf("expected ')' after %s(", cType))
				}
			} else {
				p.addError(fmt.Sprintf("expected '(' after %s", cType))
			}
			return CollectionInitNode{VarName: varName, Type: cType, Position: pos}
		}
	}

	// Standard structural mapping fallback: plain Bash assignment.
	remainder := p.currToken.Value
	return BashPassthroughNode{Code: fmt.Sprintf("%s=%s", varName, remainder), Position: pos}
}

func (p *Parser) parseMethodCall() ASTNode {
	pos := Position{Line: p.currToken.Line, Column: p.currToken.Column}
	varName := p.currToken.Value
	p.nextToken() // pass identity
	p.nextToken() // pass '.'

	method := p.currToken.Value
	p.nextToken() // pass method name

	args := []string{}
	if p.currToken.Type == TokenOpenParen {
		p.nextToken() // consume '('
		for p.currToken.Type != TokenCloseParen && p.currToken.Type != TokenEOF {
			if p.currToken.Type != TokenComma {
				args = append(args, p.currToken.Value)
			}
			p.nextToken()
		}
		if p.currToken.Type != TokenCloseParen {
			p.addError(fmt.Sprintf("unterminated argument list for %s.%s(", varName, method))
		}
	} else {
		p.addError(fmt.Sprintf("expected '(' after method name %s.%s", varName, method))
	}
	return MethodCallNode{VarName: varName, Method: method, Args: args, Position: pos}
}

func (p *Parser) parseDictAssignment() ASTNode {
	pos := Position{Line: p.currToken.Line, Column: p.currToken.Column}
	varName := p.currToken.Value
	p.nextToken() // pass identity
	p.nextToken() // pass '['

	key := p.currToken.Value
	p.nextToken() // pass key payload
	if p.currToken.Type == TokenCloseBracket {
		p.nextToken() // pass ']'
	} else {
		p.addError(fmt.Sprintf("expected ']' after key in %s[...]", varName))
	}
	if p.currToken.Type == TokenOperator && p.currToken.Value == "=" {
		p.nextToken() // pass '='
	} else {
		p.addError(fmt.Sprintf("expected '=' after %s[%s]", varName, key))
	}

	val := p.currToken.Value
	return DictAssignNode{VarName: varName, Key: key, Value: val, Position: pos}
}

func (p *Parser) parseForLoop() ASTNode {
	pos := Position{Line: p.currToken.Line, Column: p.currToken.Column}
	p.nextToken() // consume 'for'

	loopVars := []string{}
	for p.currToken.Type != TokenKeywordIn && p.currToken.Type != TokenEOF {
		if p.currToken.Type == TokenIdentifier {
			loopVars = append(loopVars, p.currToken.Value)
		}
		p.nextToken()
	}
	if p.currToken.Type != TokenKeywordIn {
		p.addError("expected 'in' in for-loop header")
	}
	p.nextToken() // consume 'in'

	iterTarget := p.currToken.Value
	p.nextToken() // pass target identity

	if p.currToken.Type == TokenColon {
		p.nextToken() // consume ':'
	} else {
		p.addError("expected ':' after for-loop header")
	}

	body := []ASTNode{}
	for p.currToken.Type != TokenEOF {
		// TokenDone is a real keyword now, not an identifier string match,
		// so an `echo "done"` string literal inside the loop body can never
		// be confused with the loop terminator.
		if p.currToken.Type == TokenDone || p.currToken.Type == TokenEndBlock {
			break
		}
		stmt := p.parseStatement()
		if stmt != nil {
			body = append(body, stmt)
		}
		p.nextToken()
	}
	if p.currToken.Type != TokenDone && p.currToken.Type != TokenEndBlock {
		p.addError("unterminated for-loop, expected 'done'")
	}

	return ForLoopNode{LoopVars: loopVars, IterTarget: iterTarget, Body: body, Position: pos}
}

// ============================================================================
// 4. SHELL ESCAPING HELPERS
// ============================================================================

// shellSingleQuote safely escapes a string for embedding inside single
// quotes in generated Bash, using the standard '...'\''...' technique.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellDoubleQuoteEscape escapes characters that are special inside Bash
// double quotes: backslash, double-quote, dollar sign, and backtick.
func shellDoubleQuoteEscape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '"', '$', '`':
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// ============================================================================
// 5. CODE GENERATION INTERFACE
// ============================================================================

type CodeGenerator struct {
	program       *Program
	symTable      map[string]CollectionType
	helpersNeeded map[string]bool
}

func NewCodeGenerator(program *Program, symTable map[string]CollectionType) *CodeGenerator {
	return &CodeGenerator{
		program:       program,
		symTable:      symTable,
		helpersNeeded: make(map[string]bool),
	}
}

func (g *CodeGenerator) Generate() string {
	var bodyBuilder strings.Builder

	for _, node := range g.program.Nodes {
		g.generateNode(node, &bodyBuilder, "")
	}

	var headerBuilder strings.Builder
	headerBuilder.WriteString("#!/usr/bin/env bash\n")
	headerBuilder.WriteString("# Generated natively via BashForge Core v0.1 Engine.\n\n")

	if g.helpersNeeded["sort"] {
		headerBuilder.WriteString(helperSort)
	}
	if g.helpersNeeded["reverse"] {
		headerBuilder.WriteString(helperReverse)
	}

	return headerBuilder.String() + bodyBuilder.String()
}

func (g *CodeGenerator) generateNode(node ASTNode, sb *strings.Builder, indent string) {
	switch n := node.(type) {
	case BashPassthroughNode:
		sb.WriteString(indent + n.Code + "\n")

	case RawBlockNode:
		for _, line := range strings.Split(strings.TrimRight(n.InnerContent, "\n"), "\n") {
			sb.WriteString(indent + line + "\n")
		}

	case CollectionInitNode:
		if n.Type == TypeList {
			sb.WriteString(fmt.Sprintf("%sdeclare -a %s=()\n", indent, n.VarName))
		} else if n.Type == TypeDict || n.Type == TypeSet {
			sb.WriteString(fmt.Sprintf("%sdeclare -A %s=()\n", indent, n.VarName))
		}

	case DictAssignNode:
		key := shellDoubleQuoteEscape(n.Key)
		val := shellDoubleQuoteEscape(n.Value)
		sb.WriteString(fmt.Sprintf("%s%s[\"%s\"]=\"%s\"\n", indent, n.VarName, key, val))

	case MethodCallNode:
		sb.WriteString(g.generateMethodTranslation(n, indent))

	case ForLoopNode:
		g.generateForLoopTranslation(n, sb, indent)
	}
}

func (g *CodeGenerator) generateMethodTranslation(m MethodCallNode, indent string) string {
	cType := g.symTable[m.VarName]

	switch cType {
	case TypeList:
		switch m.Method {
		case "append":
			arg := shellDoubleQuoteEscape(arg0(m.Args))
			return fmt.Sprintf("%s%s+=(\"%s\")\n", indent, m.VarName, arg)
		case "clear":
			return fmt.Sprintf("%s%s=()\n", indent, m.VarName)
		case "pop":
			// Use double quotes with arithmetic expansion so the index is
			// computed, rather than single-quoting it into a literal string.
			return fmt.Sprintf("%sunset \"%s[$((${#%s[@]}-1))]\"\n", indent, m.VarName, m.VarName)
		case "sort":
			g.helpersNeeded["sort"] = true
			return fmt.Sprintf("%s__bf_sort %s\n", indent, m.VarName)
		case "reverse":
			g.helpersNeeded["reverse"] = true
			return fmt.Sprintf("%s__bf_reverse %s\n", indent, m.VarName)
		}

	case TypeDict:
		switch m.Method {
		case "remove":
			key := shellSingleQuote(arg0(m.Args))
			return fmt.Sprintf("%sunset %s[%s]\n", indent, m.VarName, key)
		}

	case TypeSet:
		switch m.Method {
		case "add":
			arg := shellDoubleQuoteEscape(arg0(m.Args))
			return fmt.Sprintf("%s%s[\"%s\"]=1\n", indent, m.VarName, arg)
		case "remove":
			key := shellSingleQuote(arg0(m.Args))
			return fmt.Sprintf("%sunset %s[%s]\n", indent, m.VarName, key)
		}
	}

	return fmt.Sprintf("%s# Unresolved method translation: %s.%s\n", indent, m.VarName, m.Method)
}

func arg0(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func (g *CodeGenerator) generateForLoopTranslation(f ForLoopNode, sb *strings.Builder, indent string) {
	cType := g.symTable[f.IterTarget]

	if cType == TypeDict && len(f.LoopVars) >= 2 {
		keyVar := f.LoopVars[0]
		valVar := f.LoopVars[1]
		sb.WriteString(fmt.Sprintf("%sfor %s in \"${!%s[@]}\"; do\n", indent, keyVar, f.IterTarget))
		// 'local' removed: it is only valid inside a function. Generated
		// top-level scripts must use plain assignment instead.
		sb.WriteString(fmt.Sprintf("%s  %s=\"${%s[$%s]}\"\n", indent, valVar, f.IterTarget, keyVar))
	} else {
		loopVar := "item"
		if len(f.LoopVars) > 0 {
			loopVar = f.LoopVars[0]
		}
		sb.WriteString(fmt.Sprintf("%sfor %s in \"${%s[@]}\"; do\n", indent, loopVar, f.IterTarget))
	}

	innerIndent := indent + "  "
	for _, subNode := range f.Body {
		g.generateNode(subNode, sb, innerIndent)
	}
	sb.WriteString(indent + "done\n")
}

const helperSort = `__bf_sort() {
  local -n arr=$1
  local IFS=$'\n'
  local sorted=($(sort <<<"${arr[*]}"))
  arr=("${sorted[@]}")
}

`

const helperReverse = `__bf_reverse() {
  local -n arr=$1
  local i j tmp
  for ((i=0, j=${#arr[@]}-1; i<j; i++, j--)); do
    tmp=${arr[i]}
    arr[i]=${arr[j]}
    arr[j]=$tmp
  done
}

`

// ============================================================================
// 6. TEST PLATFORM & EXECUTION ENTRYPOINT
// ============================================================================

func main() {
	sampleSource := `
# Setup dynamic environment targets
TARGET_ENV="production"
echo "Initializing orchestration targets for $TARGET_ENV..."

servers = list()
hosts = dict()
failed = set()

servers.append("web01")
servers.append("db01")
servers.append("gateway")
servers.sort()

hosts["web01"] = "10.0.0.10"
hosts["db01"] = "10.0.0.11"

failed.add("db01")

for server in servers:
    echo "Processing host vector matching node identity: $server"
done

for host, ip in hosts:
    echo "Verifying baseline ping pipeline integrity tracking: $host maps to $ip"
done

__raw__:
if [[ -d "/proc" ]]; then
    echo "System state validation trace path checks verified successfully."
fi
__end__
`

	fmt.Println("--- BASHFORGE DIALECT INPUT SOURCE ---")
	fmt.Println(strings.TrimSpace(sampleSource))
	fmt.Println("\n--------------------------------------")

	lexer := NewLexer(sampleSource)
	parser := NewParser(lexer)
	program := parser.Parse()

	if errs := parser.Errors(); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "--- PARSE ERRORS ---")
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e.String())
		}
		fmt.Fprintln(os.Stderr, "---------------------")
	}

	generator := NewCodeGenerator(program, parser.symTable)
	compiledBash := generator.Generate()

	fmt.Println("--- COMPILED STANDARD BASH RUNTIME ENVIRONMENT OUTPUT ---")
	fmt.Print(compiledBash)
	fmt.Println("---------------------------------------------------------")
}
