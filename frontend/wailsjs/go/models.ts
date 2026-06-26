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

export namespace session {
	
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
	
	    static createFrom(source: any = {}) {
	        return new SessionSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Title = source["Title"];
	    }
	}
	

}

