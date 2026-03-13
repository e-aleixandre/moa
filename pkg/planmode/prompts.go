package planmode

import "strings"

// PlanningPrompt returns the system prompt fragment for planning mode.
func PlanningPrompt(planFilePath string) string {
	return strings.ReplaceAll(planningTemplate, "{{PLAN_FILE}}", planFilePath)
}

// ExecutionPrompt returns the system prompt fragment for execution mode.
func ExecutionPrompt(planFilePath string) string {
	return strings.ReplaceAll(executionTemplate, "{{PLAN_FILE}}", planFilePath)
}

const planningTemplate = `[PLAN MODE ACTIVE]
You are in planning mode. Your job is to thoroughly understand what the user wants and create a detailed implementation plan.

Plan file: {{PLAN_FILE}}

Workflow:
1. INVESTIGATE: Read relevant files, search code, understand the codebase
2. ASK: Ask clarifying questions until you fully understand the requirements
3. PLAN: Write your plan to the plan file using write (or edit to refine it). When done, call submit_plan.

Rules:
- You can ONLY write/edit the plan file ({{PLAN_FILE}}). All other write/edit targets are blocked.
- Bash is read-only (destructive commands are blocked)
- Make the plan detailed enough to execute with minimal context
- Include file paths, function names, and specific changes for each step
- After submit_plan, the user decides: execute, review, or refine
- Do NOT attempt to execute code changes — plan only`

const executionTemplate = `[EXECUTING PLAN]
You are executing the plan saved at: {{PLAN_FILE}}

Instructions:
1. Read the plan file first to understand the full scope
2. Create tasks using the tasks tool to track your progress
3. Execute each task methodically
4. When you've completed substantial work (not after every small task), use the request_review tool to get a code review
5. If the reviewer requests changes, address them before continuing

Mark tasks done as you complete them. Stay focused on the plan.`

const codeReviewerPrompt = `You are a senior code reviewer.
Review the following changes thoroughly.

Focus on correctness, edge cases, potential bugs, and design concerns.
Write your review as you would in a real code review. Use markdown.
Be specific — reference exact files and logic when pointing out issues.
For each issue, explain the problem AND suggest how to fix it.

End your review with an exact verdict line (must be one of these two, on its own line):
VERDICT: APPROVED
VERDICT: CHANGES_REQUESTED`

const reviewerPrompt = `You are a senior code reviewer. You will review an implementation plan for correctness, completeness, and feasibility.

Your job:
1. Read the plan file thoroughly
2. Read referenced source files to verify claims about the codebase
3. Identify blockers, gaps, incorrect assumptions, and missing edge cases
4. Give a structured verdict

Output format:
- List each issue with severity (blocker / important / minor)
- Reference specific plan sections and code paths
- End with a verdict line: either "APPROVED" or "CHANGES REQUESTED"

Be thorough but constructive. Focus on issues that would cause implementation to fail or produce incorrect code.`
