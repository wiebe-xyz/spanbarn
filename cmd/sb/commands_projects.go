package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type projectRow struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func listProjects(c *Client) ([]projectRow, error) {
	data, err := c.get("/api/v1/projects")
	if err != nil {
		return nil, err
	}
	var projects []projectRow
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, fmt.Errorf("parse projects: %w", err)
	}
	return projects, nil
}

func validateProject(c *Client, slug string) error {
	projects, err := listProjects(c)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if p.Slug == slug {
			return nil
		}
	}
	return fmt.Errorf("project %q not found — run 'sb projects' to list available projects", slug)
}

// pickProject prints a numbered list and reads a selection from stdin.
func pickProject(c *Client) (string, error) {
	projects, err := listProjects(c)
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "", fmt.Errorf("no projects found")
	}
	if len(projects) == 1 {
		return projects[0].Slug, nil
	}
	fmt.Fprintln(os.Stderr, "Select a project:")
	for i, p := range projects {
		fmt.Fprintf(os.Stderr, "  %d) %s (%s)\n", i+1, p.Slug, p.Name)
	}
	fmt.Fprint(os.Stderr, "> ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(projects) {
		return "", fmt.Errorf("invalid selection")
	}
	return projects[n-1].Slug, nil
}

func cmdProjects(args []string) error {
	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	data, err := client.get("/api/v1/projects")
	if err != nil {
		return err
	}
	return emit(data)
}
