// Noise filtering and tier classification: which paths are worth indexing, and in what order a
// composer should read them.
//
// The filter is the highest-leverage cost control in the connector. A dependency directory or a
// lockfile costs the same tokens to derive as a source file and tells a reader nothing about what
// the repository does, so dropping it before it becomes a fragment removes the cost rather than
// deferring it. The lists are patterns rather than a hand-rolled walk because doublestar is
// already the matcher config validation and view scoping use, so a glob means the same thing in
// all three places.
//
// Tiers are the envelope's composition order: docs first, because a repository's own description
// of itself is the most information per token, then entrypoints, then declarations, then
// implementation. The first matching group wins, so the lists are ordered by specificity.
package github

import (
	"mime"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/maxwellcudlitz/inget/internal/source"
)

// skipPatterns drop paths that cost tokens and carry no signal about what a repository does.
// Five groups: content that is not text, code this repository did not write, generated output,
// tests and fixtures, and files whose only content is a version number.
var skipPatterns = []string{
	// Binary, media and archive content.
	"**/*.{png,jpg,jpeg,gif,bmp,ico,svg,webp,tiff,psd,ai}",
	"**/*.{mp3,mp4,wav,ogg,webm,mov,avi,mkv,flac}",
	"**/*.{zip,tar,gz,tgz,bz2,xz,7z,rar,jar,war,whl,deb,rpm,dmg,iso}",
	"**/*.{woff,woff2,ttf,otf,eot}",
	"**/*.{pdf,doc,docx,xls,xlsx,ppt,pptx}",
	"**/*.{so,dylib,dll,exe,a,o,obj,class,pyc,pyo,wasm,bin,dat,db,sqlite,sqlite3}",
	"**/*.{pem,key,crt,cer,pfx,p12,keystore,jks}",

	// Dependencies and build output: code this repository did not write.
	"**/node_modules/**", "**/vendor/**", "**/.git/**", "**/.svn/**",
	"**/dist/**", "**/build/**", "**/target/**", "**/out/**", "**/bin/**", "**/obj/**",
	"**/.venv/**", "**/venv/**", "**/site-packages/**", "**/__pycache__/**",
	"**/.next/**", "**/.nuxt/**", "**/.terraform/**", "**/.gradle/**", "**/.idea/**",
	"**/.mypy_cache/**", "**/.pytest_cache/**", "**/.ruff_cache/**", "**/coverage/**",

	// Generated and minified output.
	"**/*.min.{js,css}", "**/*.map", "**/*.pb.go", "**/*.pb.gw.go",
	"**/*_generated.go", "**/*.generated.*", "**/*_pb2.py", "**/*_pb2_grpc.py",
	"**/zz_generated*", "**/*.g.dart", "**/*.freezed.dart",

	// Tests, fixtures and mocks: they describe the tests, not the thing under test.
	"**/testdata/**", "**/test/**", "**/tests/**", "**/__tests__/**", "**/spec/**",
	"**/fixtures/**", "**/mocks/**", "**/__mocks__/**",
	"**/*_test.go", "**/*_test.py", "**/test_*.py",
	"**/*.{test,spec}.{js,jsx,ts,tsx}",

	// Lockfiles: thousands of lines of resolved versions, no design information. The
	// manifest beside each one is kept and says what the project actually depends on.
	"**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml", "**/npm-shrinkwrap.json",
	"**/go.sum", "**/Cargo.lock", "**/poetry.lock", "**/Gemfile.lock", "**/composer.lock",
	"**/uv.lock", "**/*.lock", "**/*.lockb",

	// Legal and convention boilerplate, identical across millions of repositories.
	"LICENSE*", "COPYING*", "NOTICE*", "**/CHANGELOG*", "**/.DS_Store",
	"**/.gitignore", "**/.gitattributes", "**/.gitmodules", "**/.mailmap",
	"**/.editorconfig", "**/.dockerignore", "**/.npmrc", "**/.nvmrc",
}

// tierPatterns classify surviving paths, first match winning. The order encodes information
// density: what the repository says about itself, then where it starts, then what it declares,
// then what it implements.
var tierPatterns = []struct {
	tier     int
	patterns []string
}{
	{source.TierDocs, []string{
		// "doc/**" is deliberately absent: Go and Python both use a package named doc, so
		// the directory alone does not mean documentation. Extension carries that.
		"README*", "**/README*", "AGENTS.md", "CONTRIBUTING*", "ARCHITECTURE*",
		"docs/**/*.{md,mdx,rst,adoc,txt}", "**/*.{md,mdx,rst,adoc,txt}",
	}},
	{source.TierEntrypoints, []string{
		// Extensions are enumerated rather than globbed: "main.*" would claim main.tf and
		// main.css, which are a declaration and a stylesheet, not entrypoints.
		"cmd/**", "**/main.{go,py,rs,c,cc,cpp,java,kt,ts,tsx,js,rb,swift,cs,ex,zig,dart}",
		"**/index.{js,jsx,ts,tsx,mjs,cjs}", "**/__main__.py",
		"**/app.{js,ts,py,rb,go}", "**/server.{js,ts,py,rb,go}",
		"**/cli.{go,py,ts,js,rb,rs}", "**/wsgi.py", "**/asgi.py",
	}},
	{source.TierConfig, []string{
		"go.mod", "package.json", "pyproject.toml", "requirements*.txt", "setup.{py,cfg}",
		"Cargo.toml", "pom.xml", "build.gradle*", "Gemfile", "composer.json", "*.csproj",
		"Dockerfile*", "**/Dockerfile*", "docker-compose*", "Makefile", "**/Makefile",
		"**/*.tf", "**/*.tfvars", "helm/**", "charts/**", "k8s/**", "kubernetes/**", "deploy/**",
		".github/**", "**/*.{yaml,yml,toml,ini,cfg,conf,properties}", "**/*.proto", "**/*.graphql",
		"openapi*", "swagger*",
	}},
	{source.TierSource, []string{
		"**/*.{go,py,ts,tsx,js,jsx,mjs,cjs,rs,java,kt,kts,scala,rb,php,cs,fs,swift,m,mm}",
		"**/*.{c,cc,cpp,cxx,h,hh,hpp,hxx,sh,bash,zsh,ps1,pl,lua,ex,exs,erl,clj,dart,zig,sql}",
		"**/*.{html,htm,css,scss,sass,less,vue,svelte}",
	}},
}

// mimeOverrides fill in the types Go's table does not know or gets wrong for source files. The
// value is best-effort metadata, not a decision input, so a missing entry is harmless.
var mimeOverrides = map[string]string{
	".go": "text/x-go", ".py": "text/x-python", ".rs": "text/x-rust",
	".ts": "text/x-typescript", ".tsx": "text/x-typescript", ".jsx": "text/javascript",
	".md": "text/markdown", ".mdx": "text/markdown", ".rst": "text/x-rst",
	".tf": "text/x-terraform", ".proto": "text/x-protobuf", ".graphql": "application/graphql",
	".yaml": "application/yaml", ".yml": "application/yaml", ".toml": "application/toml",
	".sql": "application/sql", ".sh": "text/x-shellscript", ".java": "text/x-java",
	".rb": "text/x-ruby", ".kt": "text/x-kotlin", ".c": "text/x-c", ".h": "text/x-c",
	".cpp": "text/x-c++", ".cs": "text/x-csharp", ".php": "text/x-php",
}

// keepPath reports whether a repository path should become a fragment.
func keepPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "../") {
		return false
	}
	for _, pattern := range skipPatterns {
		if matchPath(pattern, p) {
			return false
		}
	}
	// A path that matches no tier group is tier "other" and is still kept: an unfamiliar
	// extension is more likely a language this list has not learned than noise.
	return true
}

// classify returns the composition tier of a path.
func classify(p string) int {
	for _, group := range tierPatterns {
		for _, pattern := range group.patterns {
			if matchPath(pattern, p) {
				return group.tier
			}
		}
	}
	return source.TierOther
}

// matchPath matches a glob against a path, and against its basename when the pattern has no
// separator. Without the second case a pattern like "Makefile" would match only at the
// repository root, which is not what a reader of the list expects.
func matchPath(pattern, p string) bool {
	if ok, err := doublestar.Match(pattern, p); err == nil && ok {
		return true
	}
	if strings.Contains(pattern, "/") {
		return false
	}
	ok, err := doublestar.Match(pattern, path.Base(p))
	return err == nil && ok
}

// mimeType is a best-effort content type for a path.
func mimeType(p string) string {
	ext := strings.ToLower(path.Ext(p))
	if override, ok := mimeOverrides[ext]; ok {
		return override
	}
	if byExt := mime.TypeByExtension(ext); byExt != "" {
		// Strip any charset parameter: the envelope's mime field is a type, not a header.
		if semicolon := strings.Index(byExt, ";"); semicolon >= 0 {
			return strings.TrimSpace(byExt[:semicolon])
		}
		return byExt
	}
	return ""
}
