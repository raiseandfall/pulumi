// Copyright 2016-2018, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logging

// Wrapper around the glog API that allows us to intercept all logging calls and manipulate them as
// necessary.  This is primarily used so we can make a best effort approach to filtering out secrets
// from any logs we emit before they get written to log-files/stderr.
//
// Code in pulumi should use this package instead of directly importing glog itself.  If any glog
// methods are needed that are not exported from this, they can be added, with the caveat that they
// should be updated to properly filter as well before forwarding things along.

import (
	"bytes"
	"flag"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/golang/glog"
)

type Filter interface {
	Filter(s string) string
}

var LogToStderr = false // true if logging is being redirected to stderr.
var Verbose = 0         // >0 if verbose logging is enabled at a particular level.
var LogFlow = false     // true to flow logging settings to child processes.

var rwLock sync.RWMutex
var filters []Filter

func V(level glog.Level) glog.Verbose {
	return glog.V(level)
}

func Errorf(format string, args ...interface{}) {
	glog.Errorf("%s", FilterString(fmt.Sprintf(format, args...)))
}

func Infof(format string, args ...interface{}) {
	glog.Infof("%s", FilterString(fmt.Sprintf(format, args...)))
}

func Warningf(format string, args ...interface{}) {
	glog.Warningf("%s", FilterString(fmt.Sprintf(format, args...)))
}

func Flush() {
	glog.Flush()
}

// InitLogging ensures the logging library has been initialized with the given settings.
func InitLogging(logToStderr bool, verbose int, logFlow bool) {
	// Remember the settings in case someone inquires.
	LogToStderr = logToStderr
	Verbose = verbose
	LogFlow = logFlow

	// Ensure the logging library has been initialized, including calling flag.Parse beforehand.  Unfortunately,
	// this is the only way to control the way logging runs.  That includes poking around at flags below.
	flag.Parse()
	if logToStderr {
		err := flag.Lookup("logtostderr").Value.Set("true")
		assertNoError(err)
	}
	if verbose > 0 {
		err := flag.Lookup("v").Value.Set(strconv.Itoa(verbose))
		assertNoError(err)
	}
}

func assertNoError(err error) {
	if err != nil {
		failfast(err.Error())
	}
}

func failfast(msg string) {
	panic(fmt.Sprintf("fatal: %v", msg))
}

type nopFilter struct {
}

func (f *nopFilter) Filter(s string) string {
	return s
}

type regexFilter struct {
	re          *regexp.Regexp
	replacement string
}

func (f *regexFilter) Filter(s string) string {
	return f.re.ReplaceAllLiteralString(s, f.replacement)
}

func AddGlobalFilter(filter Filter) {
	rwLock.Lock()
	filters = append(filters, filter)
	rwLock.Unlock()
}

func CreateFilter(secrets []string, replacement string) Filter {
	var b bytes.Buffer
	for _, secret := range secrets {
		// For short secrets, don't actually add them to the filter, this is a trade-off we make to prevent
		// displaying `[secret]`. Travis does a similar thing, for example.
		if len(secret) < 3 {
			continue
		}
		if b.Len() > 0 {
			b.WriteRune('|')
		}

		b.WriteString(regexp.QuoteMeta(secret))
	}
	// "[secret]"
	if b.Len() > 0 {
		return &regexFilter{re: regexp.MustCompile(b.String()), replacement: replacement}
	}

	return &nopFilter{}
}

func FilterString(msg string) string {
	var localFilters []Filter
	rwLock.RLock()
	localFilters = filters
	rwLock.RUnlock()

	for _, filter := range localFilters {
		msg = filter.Filter(msg)
	}

	return msg
}
