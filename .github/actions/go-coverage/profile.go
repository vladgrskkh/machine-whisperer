package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const atomicHeader = "mode: atomic"

var profileLine = regexp.MustCompile(`^(.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+) ([0-9]+) ([0-9]+)$`)

type block struct {
	statements int
	hits       int
}

func main() {
	module := flag.String("module", "", "import path of the module in the current directory")
	flag.Parse()
	if *module == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: profile -module <module> <profile>...")
		os.Exit(2)
	}
	if err := mergeProfiles(os.Stdout, ".", *module, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mergeProfiles(out io.Writer, root, module string, inputs []string) error {
	blocks := make(map[string]block)
	generated := make(map[string]bool)
	for _, input := range inputs {
		if err := readProfile(input, root, module, blocks, generated); err != nil {
			return fmt.Errorf("read coverage profile %s: %w", input, err)
		}
	}

	writer := bufio.NewWriter(out)
	if _, err := fmt.Fprintln(writer, atomicHeader); err != nil {
		return err
	}
	for _, location := range slices.Sorted(maps.Keys(blocks)) {
		block := blocks[location]
		if _, err := fmt.Fprintf(writer, "%s %d %d\n", location, block.statements, block.hits); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func readProfile(input, root, module string, blocks map[string]block, generated map[string]bool) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() || scanner.Text() != atomicHeader {
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("expected %q header", atomicHeader)
	}
	for scanner.Scan() {
		line := scanner.Text()
		match := profileLine.FindStringSubmatch(line)
		if match == nil {
			return fmt.Errorf("invalid block %q", line)
		}
		location := match[1]
		filename := location[:strings.LastIndexByte(location, ':')]
		if _, ok := generated[filename]; !ok {
			isGenerated, err := generatedSource(root, module, filename)
			if err != nil {
				return err
			}
			generated[filename] = isGenerated
		}
		if generated[filename] {
			continue
		}

		statements, err := strconv.Atoi(match[2])
		if err != nil {
			return fmt.Errorf("invalid statement count in block %q: %w", location, err)
		}
		hits, err := strconv.Atoi(match[3])
		if err != nil {
			return fmt.Errorf("invalid hit count in block %q: %w", location, err)
		}
		if previous, ok := blocks[location]; ok {
			if previous.statements != statements {
				return fmt.Errorf("inconsistent statement count for block %q", location)
			}
			if hits > math.MaxInt-previous.hits {
				return fmt.Errorf("hit count overflow for block %q", location)
			}
			hits += previous.hits
		}
		blocks[location] = block{statements: statements, hits: hits}
	}
	return scanner.Err()
}

func generatedSource(root, module, filename string) (bool, error) {
	relative, ok := strings.CutPrefix(filename, module+"/")
	if !ok {
		return false, fmt.Errorf("source %q is outside module %q", filename, module)
	}
	local, err := filepath.Localize(relative)
	if err != nil {
		return false, fmt.Errorf("invalid source path %q: %w", filename, err)
	}
	source, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, local), nil, parser.ParseComments|parser.PackageClauseOnly|parser.SkipObjectResolution)
	if err != nil {
		return false, err
	}
	return ast.IsGenerated(source), nil
}
