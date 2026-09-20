/*
 * AMF Configuration Factory
 */

package factory

import (
	"strings"
	"testing"

	"github.com/asaskevich/govalidator"
)

func TestSctp_validate(t *testing.T) {
	type fields struct {
		NumOstreams    uint
		MaxInstreams   uint
		MaxAttempts    uint
		MaxInitTimeout uint
	}
	tests := []struct {
		name    string
		fields  fields
		want    bool
		wantErr bool
		numErr  int
	}{
		// TODO: Add test cases.
		{
			name: "test OK -- Max",
			fields: fields{
				NumOstreams:    10,
				MaxInstreams:   10,
				MaxAttempts:    5,
				MaxInitTimeout: 5,
			},
			want:    true,
			wantErr: false,
			numErr:  0,
		},
		{
			name: "test OK -- Min",
			fields: fields{
				NumOstreams:    1,
				MaxInstreams:   1,
				MaxAttempts:    1,
				MaxInitTimeout: 1,
			},
			want:    true,
			wantErr: false,
			numErr:  0,
		},
		{
			name: "test Error -- zeros",
			fields: fields{
				NumOstreams:    0,
				MaxInstreams:   0,
				MaxAttempts:    0,
				MaxInitTimeout: 0,
			},
			want:    false,
			wantErr: true,
			numErr:  4,
		},
		{
			name: "test Error -- upperbound",
			fields: fields{
				NumOstreams:    11,
				MaxInstreams:   11,
				MaxAttempts:    6,
				MaxInitTimeout: 6,
			},
			want:    false,
			wantErr: true,
			numErr:  4,
		},
		{
			name: "test Error -- not set",
			fields: fields{
				MaxInstreams: 10,
			},
			want:    false,
			wantErr: true,
			numErr:  3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &Sctp{
				NumOstreams:    tt.fields.NumOstreams,
				MaxInstreams:   tt.fields.MaxInstreams,
				MaxAttempts:    tt.fields.MaxAttempts,
				MaxInitTimeout: tt.fields.MaxInitTimeout,
			}
			got, err := n.validate()

			if (err != nil) != tt.wantErr {
				t.Errorf("Sctp.validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				errs := err.(govalidator.Errors)
				if len(errs) != tt.numErr {
					t.Errorf("Sctp.validate() error = %v, wantErr %v", err, tt.wantErr)
					return
				}
			}
			if got != tt.want {
				t.Errorf("Sctp.validate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The dispatch mode is the one knob that selects which arm of the benchmark
// runs, so a typo in it must stop the AMF rather than silently produce results
// for a different policy than the one the run is labelled with.
func TestNgapSchedulerMode_validate(t *testing.T) {
	tests := []struct {
		mode string
		ok   bool
	}{
		{NgapSchedulerModeBlog, true},
		{NgapSchedulerModePaper, true},
		{NgapSchedulerModePaperEarly, true},
		{"", true}, // omitted: GetNgapSchedulerMode() defaults to blog
		{"Paper", false},
		{"paper_early", false},
		{"paperearly", false},
		{"nonsense", false},
	}

	for _, tt := range tests {
		name := tt.mode
		if name == "" {
			name = "(unset)"
		}
		t.Run(name, func(t *testing.T) {
			// A bare Configuration fails every other required field, so look
			// only at whether this one field was reported.
			c := &Configuration{NgapSchedulerMode: tt.mode}
			_, err := govalidator.ValidateStruct(c)
			reported := err != nil && strings.Contains(err.Error(), "NgapSchedulerMode")

			if tt.ok && reported {
				t.Errorf("mode %q should be accepted, got: %v", tt.mode, err)
			}
			if !tt.ok && !reported {
				t.Errorf("mode %q should be rejected, but the validator did not report it", tt.mode)
			}
		})
	}
}

func TestGetNgapSchedulerMode_defaultsToBlog(t *testing.T) {
	c := &Config{Configuration: &Configuration{}}
	if got := c.GetNgapSchedulerMode(); got != NgapSchedulerModeBlog {
		t.Errorf("unset mode should default to %q, got %q", NgapSchedulerModeBlog, got)
	}

	c.Configuration.NgapSchedulerMode = NgapSchedulerModePaperEarly
	if got := c.GetNgapSchedulerMode(); got != NgapSchedulerModePaperEarly {
		t.Errorf("configured mode should be returned, got %q", got)
	}
}
