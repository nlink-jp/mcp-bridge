package oauth

import (
	"reflect"
	"testing"
)

// Expected values come from the grammar of RFC 9110 §11.6.1 and the Bearer
// parameters of RFC 6750 §3; the first two rows are the shapes RFC 9728 §5.1
// and the MCP specification show.
func TestBearerParam(t *testing.T) {
	cases := []struct {
		name   string
		header []string
		param  string
		want   string
	}{
		{
			"resource_metadata alone",
			[]string{`Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`},
			"resource_metadata", "https://mcp.example.com/.well-known/oauth-protected-resource",
		},
		{
			"scope beside it",
			[]string{`Bearer resource_metadata="https://mcp.example.com/meta", scope="files:read files:write"`},
			"scope", "files:read files:write",
		},
		{
			"names are case-insensitive",
			[]string{`bearer Resource_Metadata="https://mcp.example.com/meta", SCOPE="a"`},
			"scope", "a",
		},
		{
			"an unquoted token value",
			[]string{`Bearer scope=read, error=invalid_token`},
			"scope", "read",
		},
		{
			"whitespace around the equals sign",
			[]string{`Bearer scope = "a b" , realm = "x"`},
			"scope", "a b",
		},
		{
			"an escaped quote inside a quoted string",
			[]string{`Bearer error_description="say \"hi\"", scope="a"`},
			"error_description", `say "hi"`,
		},
		{
			"text inside another parameter is not the parameter",
			[]string{`Bearer error_description="needs scope=\"admin\" or resource_metadata=\"https://evil.example.com/\""`},
			"scope", "",
		},
		{
			"same again for resource_metadata",
			[]string{`Bearer error_description="needs scope=\"admin\" or resource_metadata=\"https://evil.example.com/\""`},
			"resource_metadata", "",
		},
		{
			"a name that only ends in the name",
			[]string{`Bearer xscope="wrong"`},
			"scope", "",
		},
		{
			"another scheme's parameters are not Bearer's",
			[]string{`Basic realm="x", scope="basic-scope"`},
			"scope", "",
		},
		{
			"Bearer after another challenge in the same value",
			[]string{`Basic realm="x", Bearer scope="a"`},
			"scope", "a",
		},
		{
			"Bearer after a token68 challenge",
			[]string{`Negotiate dGVzdA==, Bearer scope="a"`},
			"scope", "a",
		},
		{
			"a second header line",
			[]string{`Basic realm="x"`, `Bearer scope="a"`},
			"scope", "a",
		},
		{
			"the first Bearer challenge that has it",
			[]string{`Bearer realm="x"`, `Bearer scope="first"`, `Bearer scope="second"`},
			"scope", "first",
		},
		{
			"an empty value counts as absent",
			[]string{`Bearer scope=""`, `Bearer scope="later"`},
			"scope", "later",
		},
		{
			"a value of only whitespace counts as absent too",
			[]string{`Bearer scope=" "`, `Bearer scope="later"`},
			"scope", "later",
		},
		{
			"a parameter given twice makes the line malformed",
			[]string{`Bearer scope="a", scope="b"`, `Bearer scope="next line"`},
			"scope", "next line",
		},
		{
			"twice under different case is still twice",
			[]string{`Bearer resource_metadata="https://a.example.com/", Resource_Metadata="https://b.example.com/"`},
			"resource_metadata", "",
		},
		{
			"the same name in two challenges is not a repeat",
			[]string{`Basic realm="x", Bearer realm="y", scope="a"`},
			"scope", "a",
		},
		{
			"a bare scheme",
			[]string{`Bearer`},
			"scope", "",
		},
		{
			"a malformed line is skipped whole, and the next one read",
			[]string{`Bearer scope="unterminated`, `Bearer scope="good"`},
			"scope", "good",
		},
		{
			"a parameter with no challenge",
			[]string{`scope="orphan"`},
			"scope", "",
		},
		{
			"a parameter with no value",
			[]string{`Bearer scope=`},
			"scope", "",
		},
		{"no header at all", nil, "scope", ""},
		{"an empty header", []string{""}, "scope", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bearerParam(tc.header, tc.param); got != tc.want {
				t.Errorf("bearerParam(%q, %q) = %q, want %q", tc.header, tc.param, got, tc.want)
			}
		})
	}
}

func TestParseChallenges(t *testing.T) {
	got, ok := parseChallenges(`Basic realm="a, b", Bearer error="invalid_token", scope="x y"`)
	if !ok {
		t.Fatal("a well-formed header was rejected")
	}
	want := []challenge{
		{scheme: "Basic", params: map[string]string{"realm": "a, b"}},
		{scheme: "Bearer", params: map[string]string{"error": "invalid_token", "scope": "x y"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}

	for _, bad := range []string{
		`Bearer scope="unterminated`,
		`Bearer scope="a" junk`,
		`=x`,
		`Bearer "quoted"`,
		`Bearer scope="a\`,
	} {
		if parsed, ok := parseChallenges(bad); ok {
			t.Errorf("%q was accepted as %+v", bad, parsed)
		}
	}
}
