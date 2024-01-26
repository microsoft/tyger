# Working with codespecs

Codespecs are a reusable specification of the code to execute as part a Tyger
run.

You can create a codespec with a command like the following:

```bash
tyger codespec create \
    negatingcodespec
    --image quay.io/linuxserver.io/ffmpeg \
    --input input \
    --output output \
    -- -i '$(INPUT_PIPE)' -vf negate -f- nut -y '$(OUTPUT_PIPE)'
```

This creates a codespec named `negatingcodespec` with an input buffer named
`input` and  and command-line arguments to the container's command that follow the
`--`.
