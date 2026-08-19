export declare function stagePlatformPackage(options: {
  template: string | URL;
  binary: string | URL;
  output: string;
  version: string;
}): Promise<{
  output: string;
  manifest: Record<string, unknown>;
}>;

export declare function stageRootPackage(options: {
  template: string | URL;
  scripts: string | URL;
  license: string | URL;
  output: string;
  version: string;
}): Promise<{
  output: string;
  manifest: Record<string, unknown>;
}>;
