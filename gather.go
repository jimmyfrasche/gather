//Command gather(1) gathers all files matching a glob pattern
//in the transitive closure of a Go package's dependencies.
//
//gather(1) is like find(1) mashed together with the list subcommand of the go(1) tool.
//It lists the files in Go package directories that match a glob pattern.
//This list can be altered by specifying which packages and what files to show.
//
//Packages can be specified by import paths, in the same format as the go(1) tool,
//including the ... syntax.
//If build tags alter the dependencies of the package, use the -tags flag, which
//behaves as the -tags flag on the various go(1) subcommands that support it.
//If no packages are specified, the current directory is used.
//
//By default, gather(1) also scans the dependencies of these packages, but filters
//out any standard library dependencies.
//To include standard library dependencies, use the -stdlib flag.
//To not scan the dependencies, use the -no-deps flag.
//
//Files are matched by globs as per godoc path/Filepath Match.
//The default glob is "*", but to specify import paths you must first
//specify the pattern.
//By default, dotfiles are excluded.
//To include dotfiles, use the -. flag.
//To exclude matched files that match a second glob, use the -exclude flag.
//
//By default, gather(1) prints the absolute path of each matched file.
//To print matched files relative to a given path, use the -rel flag.
//
//By default, gather(1) prints each matched file on its own line,
//with no shell escaping.
//If you have wonky file names use the -print0 flag, as with find(1).
//
//By default, gather(1) just prints the files with no concern for two
//files in different package directories having the same name.
//For a script that copies files from multiple directories into
//one directory, this can cause hard to track down failures.
//The -fail-on-dup flag will cause gather(1) to fail if two files
//have the same name.
//
//EXAMPLES
//
//List all non-dot files in the package contained in the current directory,
//and all of its non-standard library dependencies, relative to $GOPATH
//(assumes a single directory in $GOPATH)
//	gather -rel=$GOPATH
//
//List all css files, dot or not, in the transitive closure of the dependencies
//of the package in the current directory, relative to the current directory,
//while failing if two files share the same name.
//	gather -. -fail-on-dup -rel=. "*.css"
//
//List the absolute path of all non-dot .go files not matching "doc*.go"
//in some/package and all of it dependencies, including standard library dependencies.
//	gather -exclude "doc*.go" -stdlib "*.go" some/package
//
//List the absolute path of all dot files like ".git*" in the packages a/b/c, d/e/f, and g/h/...
//ignoring any dependencies
//	gather -. -no-deps ".git*" a/b/c d/e/f g/h/...
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/jimmyfrasche/goutil"
	"github.com/jimmyfrasche/goutil/gocli"
)

var (
	tags    = gocli.TagsFlag("")
	stdlib  = flag.Bool("stdlib", false, "include standard library packages in search")
	noDeps  = flag.Bool("no-deps", false, "do not search dependencies of specified packages")
	exclude = flag.String("exclude", "", "glob of files to exclude")
	rel     = flag.String("rel", "", "print all results relative to a given directory")
	print0  = flag.Bool("print0", false, "separate filenames by NUL")
	dot     = flag.Bool(".", false, "include dot files")
	faildup = flag.Bool("fail-on-dup", false, "fail if two files have the same name")
)

func importArgs(tags, imports []string) (goutil.Packages, error) {
	ctx := goutil.Context(tags...)
	pss, err := gocli.FirstError(gocli.Import(false, ctx, imports))
	if err != nil {
		return nil, err
	}
	return gocli.Flatten(pss), nil
}

func importDeps(ps goutil.Packages) (goutil.Packages, error) {
	var deps goutil.Packages
	for _, p := range ps {
		ds, err := p.ImportDeps()
		if err != nil {
			return nil, err
		}
		deps = append(deps, ds...)
	}

	return append(ps, deps...).Uniq(), nil
}

func match(dir, pattern string) ([]string, error) {
	p := filepath.Join(dir, pattern)
	return filepath.Glob(p)
}

func filter(paths []string, exclude string) ([]string, error) {
	if exclude == "" {
		return paths, nil
	}

	var out []string
	for _, p := range paths {
		b := filepath.Base(p)
		skip, err := filepath.Match(exclude, b)

		if err != nil {
			return nil, err
		}

		if !skip {
			out = append(out, p)
		}
	}
	return out, nil
}

type pathFormatter func(string) (string, error)

func identity(s string) (string, error) {
	return s, nil
}

func mkRel(to string) pathFormatter {
	return func(s string) (string, error) {
		return filepath.Rel(to, s)
	}
}

func mkFormatter(rel string) (pathFormatter, error) {
	if rel == "" {
		return identity, nil
	}
	rel = filepath.Clean(rel)
	//handle pwd as special case, will still barf on ..
	if rel == "." {
		var err error
		rel, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	return mkRel(rel), nil
}

func format(paths []string, f pathFormatter) ([]string, error) {
	var out []string
	for _, p := range paths {
		s, err := f(p)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

type dupcheck map[string]string

func (d dupcheck) push(p string) error {
	b := filepath.Base(p)
	if o, ok := d[b]; ok {
		return fmt.Errorf("duplicate file %s found at\n\t%s\npreviously\n\t%s", b, p, o)
	}
	d[b] = p
	return nil
}

//Usage: %name $flags import-path*
func main() {
	log.SetFlags(0)

	flag.Parse()
	args := flag.Args()

	// select pattern of files to match
	pat := "*"
	if len(args) != 0 {
		pat, args = args[0], args[1:]
	}

	// select packages
	ps, err := importArgs(*tags, args)
	if err != nil {
		log.Fatalln(err)
	}

	// load dependencies of selected packages, unless told otherwise
	if !*noDeps {
		ps, err = importDeps(ps)
		if err != nil {
			log.Fatalln(err)
		}
	}

	// filter out standard library packages, unless told otherwise
	if !*stdlib {
		ps = ps.NoStdlib()
	}

	// build our path formatter
	formatter, err := mkFormatter(*rel)
	if err != nil {
		log.Fatalln(err)
	}

	// choose our separator
	sep := "\n"
	if *print0 {
		sep = string([]rune{0})
	}

	dups := dupcheck{}
	for _, p := range ps {
		//find matching files
		ms, err := match(p.Build.Dir, pat)
		if err != nil {
			log.Fatalln(err)
		}

		// unless told otherwise, filter out dot files
		if !*dot {
			ms, err = filter(ms, ".*")
			if err != nil {
				log.Fatalln(err)
			}
		}

		// filter out any exclusions
		ms, err = filter(ms, *exclude)
		if err != nil {
			log.Fatalln(err)
		}

		// format paths
		ms, err = format(ms, formatter)
		if err != nil {
			log.Fatalln(err)
		}

		//print formatted files, error if duplicate and we're checking for them
		for _, m := range ms {
			if *faildup {
				err = dups.push(m)
				if *faildup && err != nil {
					log.Fatalln(err)
				}
			}
			fmt.Printf("%s%s", m, sep)
		}
	}
}
