package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chef-guo/agents-hive/internal/memory/nightlyeval"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("memory-nightly-eval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "json", "output format: json or junit")
	junitPath := fs.String("junit", "", "optional JUnit XML output path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	summary, err := nightlyeval.Run(context.Background(), nightlyeval.DefaultCases(), nightlyeval.DeterministicEvaluator{})
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		if err := writeJSON(stdout, summary); err != nil {
			return err
		}
	case "junit":
		if err := writeJUnit(stdout, summary); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported -format %q", *format)
	}
	if *junitPath != "" {
		if err := writeJUnitFile(*junitPath, summary); err != nil {
			return err
		}
	}
	if !summary.Passed {
		return fmt.Errorf("memory nightly eval failed: required_failed=%v token_roi=%.4f", summary.RequiredFailed, summary.MemoryTokenROI)
	}
	return nil
}

func writeJSON(w io.Writer, summary nightlyeval.Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(summary)
}

func writeJUnitFile(path string, summary nightlyeval.Summary) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeJUnit(f, summary)
}

func writeJUnit(w io.Writer, summary nightlyeval.Summary) error {
	payload := junitTestSuite{
		Name:      "memory-nightly-eval",
		Tests:     summary.CaseCount,
		Failures:  0,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	for _, result := range summary.Results {
		tc := junitTestCase{ClassName: "memory-nightly-eval", Name: result.CaseID}
		if !result.WithMemoryPassed {
			payload.Failures++
			tc.Failure = &junitFailure{Message: result.Reason, Text: result.Reason}
		}
		payload.Cases = append(payload.Cases, tc)
	}
	if !summary.Passed && payload.Failures == 0 {
		payload.Failures = 1
		payload.Cases = append(payload.Cases, junitTestCase{
			ClassName: "memory-nightly-eval",
			Name:      "summary",
			Failure:   &junitFailure{Message: "summary thresholds failed", Text: fmt.Sprintf("token_roi=%.4f", summary.MemoryTokenROI)},
		})
		payload.Tests++
	}
	_, err := io.WriteString(w, xml.Header)
	if err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}

type junitTestSuite struct {
	XMLName   xml.Name        `xml:"testsuite"`
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Timestamp string          `xml:"timestamp,attr,omitempty"`
	Cases     []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	ClassName string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr,omitempty"`
	Text    string `xml:",chardata"`
}
