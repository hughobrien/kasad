test:
    go test ./...

race:
    go test -race ./...

build:
    go build -o kasad ./cmd/kasad

# Run the hermetic CI gate on bigthink (the Mac has no nix).
gate:
    rsync -a --delete --exclude .git ./ bigthink:~/kasad-gate/
    ssh bigthink 'mkdir -p /tmp/kgate && cd ~/kasad-gate && TMPDIR=/tmp/kgate nix build --no-write-lock-file -L "path:$PWD#checks.x86_64-linux.gate" > /tmp/kgate.log 2>&1; echo "NIX EXIT=$?"; tail -30 /tmp/kgate.log'
