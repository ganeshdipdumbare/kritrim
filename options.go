package kritrim

// Option sets a per-run value. There is one option per concept; nothing
// here duplicates a Config field.
type Option func(*runOptions)

// runOptions is what a call carries. Config holds the defaults.
type runOptions struct {
	thread string
	output any
	images []Image
	user   string
	roles  []string
	// decisions resumes a paused thread, keyed by interrupt ID.
	decisions map[string]Decision
	onEvent   func(Event)

	// schema is set by a sealed host so the box requests structured output
	// without owning the caller's output type.
	schema     map[string]any
	schemaName string
}

// Thread runs on a stored conversation. Omit it for a one-off run.
func Thread(id string) Option { return func(o *runOptions) { o.thread = id } }

// Output decodes the final answer into v, which must be a pointer. One
// repair call is made when the reply does not parse.
func Output(v any) Option { return func(o *runOptions) { o.output = v } }

// Images attaches images to the user message.
func Images(images ...Image) Option {
	return func(o *runOptions) { o.images = append(o.images, images...) }
}

// User sets the identity recorded in audit entries and given to tools.
func User(id string) Option { return func(o *runOptions) { o.user = id } }

// Roles sets the roles recorded for the run.
func Roles(roles ...string) Option {
	return func(o *runOptions) { o.roles = append([]string(nil), roles...) }
}

// Decisions resumes a thread paused on interrupts. Keys are Interrupt.ID.
func Decisions(m map[string]Decision) Option {
	return func(o *runOptions) {
		o.decisions = make(map[string]Decision, len(m))
		for k, v := range m {
			o.decisions[k] = v
		}
	}
}

// OnEvent observes a run without using Stream.
func OnEvent(fn func(Event)) Option { return func(o *runOptions) { o.onEvent = fn } }

func resolveOptions(opts []Option) runOptions {
	var o runOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

func withSchema(name string, s map[string]any) Option {
	return func(o *runOptions) { o.schema, o.schemaName = s, name }
}
