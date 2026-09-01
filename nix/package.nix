{
  lib,
  buildGoModule,
  go_1_26,
  versionCheckHook,
}:

let
  version = "0.1.0";
in
# Pin the Go toolchain to 1.26 to satisfy the `go 1.26` directive in go.mod;
# a sandboxed Nix build cannot download a newer toolchain on demand.
(buildGoModule.override { go = go_1_26; }) {
  pname = "quipu";
  inherit version;

  # Only the files needed to build the binary, so unrelated changes
  # (docs, CI config, the release scaffold) don't invalidate the build.
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../pkg
    ];
  };

  vendorHash = "sha256-NudDGtWK7y6mVxUiylfAyG5pKa9ZH8KX65kO9jIM6Mo=";

  subPackages = [ "cmd/quipu" ];

  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  # Sanity-check that the built binary runs and reports its version.
  nativeBuildInputs = [ versionCheckHook ];
  doInstallCheck = true;

  meta = {
    description = "Track git worktrees and Claude Code sessions";
    longDescription = ''
      quipu is a Go CLI + TUI that tracks git worktrees living in bare
      layouts (a container directory holding `.bare/`, a `.git` pointer
      file, and one directory per worktree), mines Claude Code's own data
      files to recover each worktree's purpose and current state, and
      restarts Claude Code sessions in WezTerm -- all backed by a local
      SQLite database.
    '';
    homepage = "https://github.com/ssoriche/quipu";
    license = lib.licenses.mit;
    mainProgram = "quipu";
    maintainers = [ ];
  };
}
