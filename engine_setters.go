package workflow

// Post-creation wiring for components that may be constructed after NewEngine,
// e.g. when a host owns the Engine before it has decided which providers to
// register. Each Set* mirrors a WithX option in engine_options.go and writes
// the same field — semantics are identical, only timing differs.

func (e *Engine) SetApprovalNotifier(fn ApprovalNotifier)     { e.approvalNotifier = fn }
func (e *Engine) SetCompletionNotifier(fn CompletionNotifier) { e.completionNotifier = fn }
func (e *Engine) SetHooks(h HookPublisher)                    { e.hooks = h }

func (e *Engine) SetAgentRunner(runner AgentRunner) {
	e.executors[StepAgent] = NewAgentExecutor(runner, e.getMetrics())
}

func (e *Engine) SetSkills(sr SkillResolver) {
	if llm, ok := e.executors[StepLLM].(*LLMExecutor); ok {
		llm.SetSkills(sr)
	}
}

func (e *Engine) SetA2ACaller(caller A2ACaller) {
	e.executors[StepA2A] = NewA2AExecutor(caller, e.getMetrics())
}
