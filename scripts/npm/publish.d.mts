export interface PublishCommandResult {
  code: number;
  signal?: string | null;
  stdout: string;
  stderr: string;
}

export interface PublishResult {
  status: "already-published" | "published";
  name: string;
  version: string;
  integrity: string;
  tag: string;
}

export declare function parsePublishArguments(args: readonly string[]): {
  directory: string;
  tag: string;
};

export declare function publishPackage(options: {
  directory: string;
  tag?: string;
  run?: (command: string, args: string[]) => Promise<PublishCommandResult>;
}): Promise<PublishResult>;
