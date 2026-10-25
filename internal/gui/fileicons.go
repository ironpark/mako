package gui

import (
	"strings"

	"github.com/ironpark/ggui/ui/icons"
)

// File-type icons, after Zed's default icon theme. The association tables
// below are Zed's (crates/theme/src/icon_theme.rs, FILE_STEMS_BY_ICON_KEY and
// FILE_SUFFIXES_BY_ICON_KEY) and the lookup is its FileIcons::get_icon
// (crates/file_icons). The artwork is not Zed's: brand logos are Simple Icons
// (icons/lang-*.svg, CC0, see icons/LICENSE-simple-icons) and the generic
// categories are Lucide (ISC, see icons/LICENSE).
//
// Icons are monochrome, drawn in whatever colour the caller gives them, as
// Zed's default theme is: brand colours clash with one theme or the other and
// a tree of coloured logos is noisy.

// zedFileStems maps whole file names (or the part before the first dot) to
// an icon key.
var zedFileStems = associations([]keyed{
	{"docker", []string{"Containerfile", "Dockerfile", ".dockerignore"}},
	{"ruby", []string{"Podfile"}},
	{"heroku", []string{"Procfile"}},
})

// zedFileSuffixes maps what follows a dot in a file name to an icon key.
var zedFileSuffixes = associations([]keyed{
	{"astro", []string{"astro"}},
	{"audio", []string{"aac", "flac", "m4a", "mka", "mp3", "ogg", "opus", "wav", "wma", "wv"}},
	{"backup", []string{"bak"}},
	{"ballerina", []string{"bal"}},
	{"bicep", []string{"bicep"}},
	{"bun", []string{"lockb"}},
	{"c", []string{"c", "h"}},
	{"cairo", []string{"cairo"}},
	{"code", []string{"handlebars", "metadata", "rkt", "scm"}},
	{"coffeescript", []string{"coffee"}},
	{"cpp", []string{"c++", "h++", "cc", "cpp", "cppm", "cxx", "hh", "hpp", "hxx", "inl", "ixx"}},
	{"crystal", []string{"cr", "ecr"}},
	{"csharp", []string{"cs"}},
	{"csproj", []string{"csproj"}},
	{"css", []string{"css", "pcss", "postcss"}},
	{"cue", []string{"cue"}},
	{"dart", []string{"dart"}},
	{"diff", []string{"diff"}},
	{"docker", []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}},
	{"document", []string{"doc", "docx", "mdx", "odp", "ods", "odt", "pdf", "ppt", "pptx", "rtf", "txt", "xls", "xlsx"}},
	{"editorconfig", []string{"editorconfig"}},
	{"elixir", []string{"eex", "ex", "exs", "heex", "leex", "neex"}},
	{"elm", []string{"elm"}},
	{"erlang", []string{"Emakefile", "app.src", "erl", "escript", "hrl", "rebar.config", "xrl", "yrl"}},
	{"eslint", []string{
		"eslint.config.cjs", "eslint.config.cts", "eslint.config.js", "eslint.config.mjs",
		"eslint.config.mts", "eslint.config.ts", "eslintrc", "eslintrc.js", "eslintrc.json",
	}},
	{"font", []string{"otf", "ttf", "woff", "woff2"}},
	{"fsharp", []string{"fs"}},
	{"fsproj", []string{"fsproj"}},
	{"gitlab", []string{"gitlab-ci.yml", "gitlab-ci.yaml"}},
	{"gleam", []string{"gleam"}},
	{"go", []string{"go", "mod", "work"}},
	{"graphql", []string{"gql", "graphql", "graphqls"}},
	{"haskell", []string{"hs"}},
	{"hcl", []string{"hcl"}},
	{"helm", []string{
		"helmfile.yaml", "helmfile.yml", "Chart.yaml", "Chart.yml", "Chart.lock",
		"values.yaml", "values.yml", "requirements.yaml", "requirements.yml", "tpl",
	}},
	{"html", []string{"htm", "html"}},
	{"image", []string{
		"avif", "bmp", "gif", "heic", "heif", "ico", "j2k", "jfif", "jp2", "jpeg", "jpg",
		"jxl", "png", "psd", "qoi", "svg", "tiff", "webp",
	}},
	{"ipynb", []string{"ipynb"}},
	{"java", []string{"java"}},
	{"javascript", []string{"cjs", "js", "mjs"}},
	{"json", []string{"json", "jsonc"}},
	{"julia", []string{"jl"}},
	{"kdl", []string{"kdl"}},
	{"kotlin", []string{"kt"}},
	{"lock", []string{"lock"}},
	{"log", []string{"log"}},
	{"lua", []string{"lua"}},
	{"luau", []string{"luau"}},
	{"markdown", []string{"markdown", "md"}},
	{"metal", []string{"metal"}},
	{"nim", []string{"nim", "nims", "nimble"}},
	{"nix", []string{"nix"}},
	{"ocaml", []string{"ml", "mli", "mlx"}},
	{"odin", []string{"odin"}},
	{"php", []string{"php"}},
	{"prettier", []string{
		"prettier.config.cjs", "prettier.config.js", "prettier.config.mjs", "prettierignore",
		"prettierrc", "prettierrc.cjs", "prettierrc.js", "prettierrc.json", "prettierrc.json5",
		"prettierrc.mjs", "prettierrc.toml", "prettierrc.yaml", "prettierrc.yml",
	}},
	{"prisma", []string{"prisma"}},
	{"puppet", []string{"pp"}},
	{"python", []string{"py"}},
	{"r", []string{"r", "R"}},
	{"react", []string{"cjsx", "ctsx", "jsx", "mjsx", "mtsx", "tsx"}},
	{"roc", []string{"roc"}},
	{"ruby", []string{"rb"}},
	{"rust", []string{"rs"}},
	{"sass", []string{"sass", "scss"}},
	{"scala", []string{"scala", "sc"}},
	{"settings", []string{"conf", "ini"}},
	{"solidity", []string{"sol"}},
	{"storage", []string{
		"accdb", "csv", "dat", "db", "dbf", "dll", "fmp", "fp7", "frm", "gdb", "ib", "ldf",
		"mdb", "mdf", "myd", "myi", "pdb", "psv", "RData", "rdata", "sav", "sdf", "sql",
		"sqlite", "ssv", "tsv",
	}},
	{"stylelint", []string{
		"stylelint.config.cjs", "stylelint.config.js", "stylelint.config.mjs", "stylelintignore",
		"stylelintrc", "stylelintrc.cjs", "stylelintrc.js", "stylelintrc.json", "stylelintrc.mjs",
		"stylelintrc.yaml", "stylelintrc.yml",
	}},
	{"surrealql", []string{"surql"}},
	{"svelte", []string{"svelte"}},
	{"swift", []string{"swift"}},
	{"tcl", []string{"tcl"}},
	{"template", []string{"hbs", "plist", "xml"}},
	{"terminal", []string{
		"bash", "bash_aliases", "bash_login", "bash_logout", "bash_profile", "bashrc", "brushrc",
		"fish", "nu", "profile", "ps1", "sh", "zlogin", "zlogout", "zprofile", "zsh",
		"zsh_aliases", "zsh_histfile", "zsh_history", "zshenv", "zshrc",
	}},
	{"terraform", []string{"tf", "tfvars"}},
	{"toml", []string{"toml"}},
	{"typescript", []string{"cts", "mts", "ts"}},
	{"v", []string{"v", "vsh", "vv"}},
	{"vcs", []string{
		"COMMIT_EDITMSG", "EDIT_DESCRIPTION", "MERGE_MSG", "NOTES_EDITMSG", "TAG_EDITMSG",
		"gitattributes", "gitignore", "gitkeep", "gitmodules",
	}},
	{"vbproj", []string{"vbproj"}},
	{"video", []string{"avi", "m4v", "mkv", "mov", "mp4", "webm", "wmv"}},
	{"vs_sln", []string{"sln"}},
	{"vs_suo", []string{"suo"}},
	{"vue", []string{"vue"}},
	{"vyper", []string{"vy", "vyi"}},
	{"wgsl", []string{"wgsl"}},
	{"yaml", []string{"yaml", "yml"}},
	{"zig", []string{"zig"}},
})

// fileIconsByKey is the bundled icon (icons/<name>.svg) for each Zed icon
// key. A key missing here has no icon, so the lookup goes on as Zed's does
// for a key its theme leaves out ("backup").
var fileIconsByKey = map[string]string{
	"default": "file",

	// Simple Icons brand logos.
	"astro":        "lang-astro",
	"bun":          "lang-bun",
	"c":            "lang-c",
	"coffeescript": "lang-coffeescript",
	"cpp":          "lang-cpp",
	"crystal":      "lang-crystal",
	"csharp":       "lang-dotnet",
	"csproj":       "lang-dotnet",
	"css":          "lang-css",
	"dart":         "lang-dart",
	"docker":       "lang-docker",
	"editorconfig": "lang-editorconfig",
	"elixir":       "lang-elixir",
	"elm":          "lang-elm",
	"erlang":       "lang-erlang",
	"eslint":       "lang-eslint",
	"fsharp":       "lang-fsharp",
	"fsproj":       "lang-dotnet",
	"gitlab":       "lang-gitlab",
	"go":           "lang-go",
	"graphql":      "lang-graphql",
	"haskell":      "lang-haskell",
	"hcl":          "lang-hcl",
	"helm":         "lang-helm",
	"heroku":       "lang-heroku",
	"html":         "lang-html",
	"ipynb":        "lang-jupyter",
	"java":         "lang-java",
	"javascript":   "lang-javascript",
	"julia":        "lang-julia",
	"kotlin":       "lang-kotlin",
	"lua":          "lang-lua",
	"luau":         "lang-lua",
	"markdown":     "lang-markdown",
	"nim":          "lang-nim",
	"nix":          "lang-nix",
	"ocaml":        "lang-ocaml",
	"odin":         "lang-odin",
	"php":          "lang-php",
	"prettier":     "lang-prettier",
	"prisma":       "lang-prisma",
	"puppet":       "lang-puppet",
	"python":       "lang-python",
	"r":            "lang-r",
	"react":        "lang-react",
	"ruby":         "lang-ruby",
	"rust":         "lang-rust",
	"sass":         "lang-sass",
	"scala":        "lang-scala",
	"solidity":     "lang-solidity",
	"stylelint":    "lang-stylelint",
	"surrealql":    "lang-surrealql",
	"svelte":       "lang-svelte",
	"swift":        "lang-swift",
	"terraform":    "lang-terraform",
	"toml":         "lang-toml",
	"typescript":   "lang-typescript",
	"v":            "lang-v",
	"vbproj":       "lang-dotnet",
	"vcs":          "lang-git",
	"vs_sln":       "lang-dotnet",
	"vue":          "lang-vue",
	"wgsl":         "lang-wgsl",
	"yaml":         "lang-yaml",
	"zig":          "lang-zig",

	// Lucide categories.
	"audio":    "file-audio",
	"code":     "file-code",
	"diff":     "file-diff",
	"document": "file-text",
	"font":     "type",
	"image":    "file-image",
	"json":     "file-json",
	"lock":     "lock",
	"log":      "info",
	"settings": "file-cog",
	"storage":  "database",
	"template": "code-xml",
	"terminal": "terminal",
	"video":    "file-video",
	"vs_suo":   "file",
	// Languages Simple Icons has no logo for.
	"ballerina": "file-code",
	"bicep":     "file-code",
	"cairo":     "file-code",
	"cue":       "file-code",
	"gleam":     "file-code",
	"kdl":       "file-code",
	"metal":     "file-code",
	"roc":       "file-code",
	"tcl":       "file-code",
	"vyper":     "file-code",
}

type keyed struct {
	key   string
	names []string
}

func associations(table []keyed) map[string]string {
	m := map[string]string{}
	for _, k := range table {
		for _, n := range k.names {
			m[n] = k.key
		}
	}
	return m
}

// fileIconName is the bundled icon a file of this name shows: Zed's lookup
// over the whole name, then each suffix after a dot, longest first, then the
// part before the first dot ("Dockerfile.prod"). Zed matches case-sensitively;
// where that finds nothing, the lowercased name gets one more try, so that
// "IMG_0001.JPG" still reads as an image.
func fileIconName(name string) string {
	if icon, ok := zedFileIcon(name); ok {
		return icon
	}
	if lower := strings.ToLower(name); lower != name {
		if icon, ok := zedFileIcon(lower); ok {
			return icon
		}
	}
	return fileIconsByKey["default"]
}

func zedFileIcon(name string) (string, bool) {
	lookup := func(s string) (string, bool) {
		key, ok := zedFileStems[s]
		if !ok {
			key, ok = zedFileSuffixes[s]
		}
		if !ok {
			return "", false
		}
		icon, ok := fileIconsByKey[key]
		return icon, ok
	}
	if icon, ok := lookup(name); ok {
		return icon, true
	}
	rest, first, split := name, "", false
	for {
		prefix, suffix, ok := strings.Cut(rest, ".")
		if !ok {
			break
		}
		if !split {
			first, split = prefix, true
		}
		if icon, ok := lookup(suffix); ok {
			return icon, true
		}
		rest = suffix
	}
	if split {
		return lookup(first)
	}
	return "", false
}

// fileTypeIcon is the icon the file tree shows ahead of a name: a folder,
// open or closed, or the file's type.
func fileTypeIcon(name string, dir, open bool) *icons.Widget {
	switch {
	case dir && open:
		return icon("folder-open")
	case dir:
		return icon("folder")
	}
	return icon(fileIconName(name))
}
