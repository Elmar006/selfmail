package mta

import (
	"bufio"
	"strings"
)

func bufReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }
