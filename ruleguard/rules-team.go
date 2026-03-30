//go:build ruleguard

package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

func receiverNameMinLength(m dsl.Matcher) {
}

func forbidIgnoringJSONDecodeError(m dsl.Matcher) {
	m.Match(`_ = json.NewDecoder($*_).Decode($*_)`).Report("handle JSON decode errors explicitly")
}

func forbidIgnoringHTTPRequestBuildError(m dsl.Matcher) {
	m.Match(`$req, _ := http.NewRequest($*_)`).Report("handle HTTP request construction errors explicitly")
	m.Match(`$req, _ := http.NewRequestWithContext($*_)`).Report("handle HTTP request construction errors explicitly")
}

func forbidIgnoringAtoiErrorInBoundaryParsing(m dsl.Matcher) {
	m.Match(`$value, _ := strconv.Atoi($*_)`).Report("handle strconv.Atoi errors explicitly at request boundaries")
}
