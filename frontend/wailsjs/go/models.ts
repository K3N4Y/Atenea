export namespace command {
	
	export class Command {
	    Name: string;
	    Description: string;
	    Template: string;
	    BuiltIn: boolean;
	    Skill: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Command(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Name = source["Name"];
	        this.Description = source["Description"];
	        this.Template = source["Template"];
	        this.BuiltIn = source["BuiltIn"];
	        this.Skill = source["Skill"];
	    }
	}

}

export namespace learning {
	
	export class Evidence {
	    seq: number;
	    summary: string;
	
	    static createFrom(source: any = {}) {
	        return new Evidence(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.summary = source["summary"];
	    }
	}
	export class Candidate {
	    statement: string;
	    scope: string;
	    exceptions: string;
	    evidence: Evidence[];
	
	    static createFrom(source: any = {}) {
	        return new Candidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.statement = source["statement"];
	        this.scope = source["scope"];
	        this.exceptions = source["exceptions"];
	        this.evidence = this.convertValues(source["evidence"], Evidence);
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
	
	export class InputMessage {
	    seq: number;
	    role: string;
	    text: string;
	
	    static createFrom(source: any = {}) {
	        return new InputMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.role = source["role"];
	        this.text = source["text"];
	    }
	}
	export class Input {
	    messages: InputMessage[];
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Input(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messages = this.convertValues(source["messages"], InputMessage);
	        this.truncated = source["truncated"];
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
	
	export class Lesson {
	    id: string;
	    workspace: string;
	    runID: string;
	    candidate: Candidate;
	    enabled: boolean;
	    deleted: boolean;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Lesson(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workspace = source["workspace"];
	        this.runID = source["runID"];
	        this.candidate = this.convertValues(source["candidate"], Candidate);
	        this.enabled = source["enabled"];
	        this.deleted = source["deleted"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
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
	    inputTokens: number;
	    outputTokens: number;
	    reasoningTokens: number;
	
	    static createFrom(source: any = {}) {
	        return new Usage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.reasoningTokens = source["reasoningTokens"];
	    }
	}
	export class Run {
	    id: string;
	    workspace: string;
	    sessionID: string;
	    cutSeq: number;
	    status: string;
	    input: Input;
	    candidate?: Candidate;
	    noCandidateReason?: string;
	    providerID: string;
	    model: string;
	    usage: Usage;
	    error?: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    startedAt?: any;
	    // Go type: time
	    finishedAt?: any;
	    // Go type: time
	    decidedAt?: any;
	
	    static createFrom(source: any = {}) {
	        return new Run(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workspace = source["workspace"];
	        this.sessionID = source["sessionID"];
	        this.cutSeq = source["cutSeq"];
	        this.status = source["status"];
	        this.input = this.convertValues(source["input"], Input);
	        this.candidate = this.convertValues(source["candidate"], Candidate);
	        this.noCandidateReason = source["noCandidateReason"];
	        this.providerID = source["providerID"];
	        this.model = source["model"];
	        this.usage = this.convertValues(source["usage"], Usage);
	        this.error = source["error"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.startedAt = this.convertValues(source["startedAt"], null);
	        this.finishedAt = this.convertValues(source["finishedAt"], null);
	        this.decidedAt = this.convertValues(source["decidedAt"], null);
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

}

export namespace main {
	
	export class ActiveProvider {
	    providerID: string;
	    providerName: string;
	    model: string;
	    contextWindow: number;
	
	    static createFrom(source: any = {}) {
	        return new ActiveProvider(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.providerID = source["providerID"];
	        this.providerName = source["providerName"];
	        this.model = source["model"];
	        this.contextWindow = source["contextWindow"];
	    }
	}
	export class DeviceLogin {
	    providerID: string;
	    providerName: string;
	    userCode: string;
	    verificationURI: string;
	    expiresAt: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceLogin(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.providerID = source["providerID"];
	        this.providerName = source["providerName"];
	        this.userCode = source["userCode"];
	        this.verificationURI = source["verificationURI"];
	        this.expiresAt = source["expiresAt"];
	    }
	}
	export class ProviderEntry {
	    id: string;
	    name: string;
	    models: string[];
	    builtIn: boolean;
	    connectable: boolean;
	    connected: boolean;
	    connectKind: string;
	
	    static createFrom(source: any = {}) {
	        return new ProviderEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.models = source["models"];
	        this.builtIn = source["builtIn"];
	        this.connectable = source["connectable"];
	        this.connected = source["connected"];
	        this.connectKind = source["connectKind"];
	    }
	}

}

export namespace mcpclient {
	
	export class ServerConfig {
	    name?: string;
	    type?: string;
	    command?: string;
	    args?: string[];
	    env?: Record<string, string>;
	    cwd?: string;
	    url?: string;
	    sensitivity?: string;
	    allowedTools?: string[];
	    allowSampling?: boolean;
	    autoConnect?: boolean;
	    connectTimeoutMs?: number;
	    callTimeoutMs?: number;
	
	    static createFrom(source: any = {}) {
	        return new ServerConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	        this.cwd = source["cwd"];
	        this.url = source["url"];
	        this.sensitivity = source["sensitivity"];
	        this.allowedTools = source["allowedTools"];
	        this.allowSampling = source["allowSampling"];
	        this.autoConnect = source["autoConnect"];
	        this.connectTimeoutMs = source["connectTimeoutMs"];
	        this.callTimeoutMs = source["callTimeoutMs"];
	    }
	}
	export class ServerStatus {
	    name?: string;
	    type?: string;
	    command?: string;
	    args?: string[];
	    env?: Record<string, string>;
	    cwd?: string;
	    url?: string;
	    sensitivity?: string;
	    allowedTools?: string[];
	    allowSampling?: boolean;
	    autoConnect?: boolean;
	    connectTimeoutMs?: number;
	    callTimeoutMs?: number;
	    connected: boolean;
	    tools: number;
	    health: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	        this.cwd = source["cwd"];
	        this.url = source["url"];
	        this.sensitivity = source["sensitivity"];
	        this.allowedTools = source["allowedTools"];
	        this.allowSampling = source["allowSampling"];
	        this.autoConnect = source["autoConnect"];
	        this.connectTimeoutMs = source["connectTimeoutMs"];
	        this.callTimeoutMs = source["callTimeoutMs"];
	        this.connected = source["connected"];
	        this.tools = source["tools"];
	        this.health = source["health"];
	        this.error = source["error"];
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
	
	export class Image {
	    MediaType: string;
	    Data: number[];
	
	    static createFrom(source: any = {}) {
	        return new Image(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.MediaType = source["MediaType"];
	        this.Data = source["Data"];
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
	    Images: Image[];
	    ToolCalls: ToolCall[];
	    ToolCallID: string;
	    IsError: boolean;
	    Seq: number;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Role = source["Role"];
	        this.Text = source["Text"];
	        this.Images = this.convertValues(source["Images"], Image);
	        this.ToolCalls = this.convertValues(source["ToolCalls"], ToolCall);
	        this.ToolCallID = source["ToolCallID"];
	        this.IsError = source["IsError"];
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
	export class PromptCheckpoint {
	    ID: string;
	    Prompt: string;
	    PromptImages: Image[];
	    BeforeTree: string;
	    AfterTree: string;
	    OriginCallID: string;
	
	    static createFrom(source: any = {}) {
	        return new PromptCheckpoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Prompt = source["Prompt"];
	        this.PromptImages = this.convertValues(source["PromptImages"], Image);
	        this.BeforeTree = source["BeforeTree"];
	        this.AfterTree = source["AfterTree"];
	        this.OriginCallID = source["OriginCallID"];
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
	    CacheableInputTokens: number;
	
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
	        this.CacheableInputTokens = source["CacheableInputTokens"];
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
	    Attrs: Record<string, string>;
	    Compaction?: CompactionCheckpoint;
	    Checkpoint?: PromptCheckpoint;
	
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
	        this.Attrs = source["Attrs"];
	        this.Compaction = this.convertValues(source["Compaction"], CompactionCheckpoint);
	        this.Checkpoint = this.convertValues(source["Checkpoint"], PromptCheckpoint);
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
	    // Go type: time
	    LastActivity: any;
	
	    static createFrom(source: any = {}) {
	        return new SessionSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Title = source["Title"];
	        this.Cwd = source["Cwd"];
	        this.LastActivity = this.convertValues(source["LastActivity"], null);
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
	
	

}

export namespace workspacegit {
	
	export class Change {
	    path: string;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new Change(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.status = source["status"];
	    }
	}
	export class Status {
	    isRepo: boolean;
	    staged: Change[];
	    unstaged: Change[];
	    untracked: Change[];
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.isRepo = source["isRepo"];
	        this.staged = this.convertValues(source["staged"], Change);
	        this.unstaged = this.convertValues(source["unstaged"], Change);
	        this.untracked = this.convertValues(source["untracked"], Change);
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

}

