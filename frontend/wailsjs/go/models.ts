export namespace command {

	export class Command {
	    Name: string;
	    Description: string;
	    Template: string;

	    static createFrom(source: any = {}) {
	        return new Command(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Name = source["Name"];
	        this.Description = source["Description"];
	        this.Template = source["Template"];
	    }
	}

}

export namespace main {

	export class GitChange {
	    path: string;
	    status: string;

	    static createFrom(source: any = {}) {
	        return new GitChange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.status = source["status"];
	    }
	}
	export class GitStatus {
	    isRepo: boolean;
	    staged: GitChange[];
	    unstaged: GitChange[];
	    untracked: GitChange[];

	    static createFrom(source: any = {}) {
	        return new GitStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.isRepo = source["isRepo"];
	        this.staged = this.convertValues(source["staged"], GitChange);
	        this.unstaged = this.convertValues(source["unstaged"], GitChange);
	        this.untracked = this.convertValues(source["untracked"], GitChange);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProviderConfig {
	    kind: string;
	    baseURL: string;
	    model: string;

	    static createFrom(source: any = {}) {
	        return new ProviderConfig(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.baseURL = source["baseURL"];
	        this.model = source["model"];
	    }
	}

}

export namespace session {

	export class ContextEpoch {
	    Agent: string;
	    Model: string;
	    BaselineSeq: number;
	    Revision: number;

	    static createFrom(source: any = {}) {
	        return new ContextEpoch(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Agent = source["Agent"];
	        this.Model = source["Model"];
	        this.BaselineSeq = source["BaselineSeq"];
	        this.Revision = source["Revision"];
	    }
	}
	export class StructuredSummary {
	    current_goal: string;
	    constraints_and_instructions: string[];
	    decisions: string[];
	    completed_work: string[];
	    files_and_changes: string[];
	    relevant_tool_results: string[];
	    failures_and_attempts: string[];
	    pending_and_next_step: string[];
	    facts_not_to_reinterpret: string[];

	    static createFrom(source: any = {}) {
	        return new StructuredSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current_goal = source["current_goal"];
	        this.constraints_and_instructions = source["constraints_and_instructions"];
	        this.decisions = source["decisions"];
	        this.completed_work = source["completed_work"];
	        this.files_and_changes = source["files_and_changes"];
	        this.relevant_tool_results = source["relevant_tool_results"];
	        this.failures_and_attempts = source["failures_and_attempts"];
	        this.pending_and_next_step = source["pending_and_next_step"];
	        this.facts_not_to_reinterpret = source["facts_not_to_reinterpret"];
	    }
	}
	export class CompactionCheckpoint {
	    summary: StructuredSummary;
	    expected_epoch: ContextEpoch;
	    covered_through_seq: number;
	    anchor_user_seq: number;
	    preserved_from_seq: number;
	    model: string;
	    reason: string;
	    input_tokens_before: number;
	    estimated_tokens_after: number;

	    static createFrom(source: any = {}) {
	        return new CompactionCheckpoint(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.summary = this.convertValues(source["summary"], StructuredSummary);
	        this.expected_epoch = this.convertValues(source["expected_epoch"], ContextEpoch);
	        this.covered_through_seq = source["covered_through_seq"];
	        this.anchor_user_seq = source["anchor_user_seq"];
	        this.preserved_from_seq = source["preserved_from_seq"];
	        this.model = source["model"];
	        this.reason = source["reason"];
	        this.input_tokens_before = source["input_tokens_before"];
	        this.estimated_tokens_after = source["estimated_tokens_after"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class ToolCall {
	    ID: string;
	    Name: string;
	    Arguments: string;

	    static createFrom(source: any = {}) {
	        return new ToolCall(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Name = source["Name"];
	        this.Arguments = source["Arguments"];
	    }
	}
	export class Message {
	    ID: string;
	    Role: string;
	    Text: string;
	    ToolCalls: ToolCall[];
	    ToolCallID: string;
	    Seq: number;

	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Role = source["Role"];
	        this.Text = source["Text"];
	        this.ToolCalls = this.convertValues(source["ToolCalls"], ToolCall);
	        this.ToolCallID = source["ToolCallID"];
	        this.Seq = source["Seq"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Usage {
	    InputTokens: number;
	    OutputTokens: number;
	    ReasoningTokens: number;
	    CacheReadTokens: number;
	    CacheWriteTokens: number;

	    static createFrom(source: any = {}) {
	        return new Usage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.InputTokens = source["InputTokens"];
	        this.OutputTokens = source["OutputTokens"];
	        this.ReasoningTokens = source["ReasoningTokens"];
	        this.CacheReadTokens = source["CacheReadTokens"];
	        this.CacheWriteTokens = source["CacheWriteTokens"];
	    }
	}
	export class SessionEvent {
	    SessionID: string;
	    Seq: number;
	    Kind: string;
	    Message?: Message;
	    Text: string;
	    CallID: string;
	    ToolName: string;
	    Input: number[];
	    Usage?: Usage;
	    Error: string;
	    Diff: string;
	    Compaction?: CompactionCheckpoint;

	    static createFrom(source: any = {}) {
	        return new SessionEvent(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.SessionID = source["SessionID"];
	        this.Seq = source["Seq"];
	        this.Kind = source["Kind"];
	        this.Message = this.convertValues(source["Message"], Message);
	        this.Text = source["Text"];
	        this.CallID = source["CallID"];
	        this.ToolName = source["ToolName"];
	        this.Input = source["Input"];
	        this.Usage = this.convertValues(source["Usage"], Usage);
	        this.Error = source["Error"];
	        this.Diff = source["Diff"];
	        this.Compaction = this.convertValues(source["Compaction"], CompactionCheckpoint);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionSummary {
	    ID: string;
	    Title: string;
	    Cwd: string;

	    static createFrom(source: any = {}) {
	        return new SessionSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Title = source["Title"];
	        this.Cwd = source["Cwd"];
	    }
	}



}

