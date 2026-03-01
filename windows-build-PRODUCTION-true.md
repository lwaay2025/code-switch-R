# Windows Build (PRODUCTION=true)

- Date: 2026-03-01 20:55:35
- Command: task windows:build PRODUCTION=true
- Exit Code: 0

## Output

```text
task: [windows:common:go:mod:tidy] go mod tidy
task: Task "windows:common:generate:icons" is up to date
task: [windows:common:install:frontend:deps] npm install
task: [windows:common:go:mod:tidy] go mod tidy
npm warn cleanup Failed to remove some directories [
npm warn cleanup   [
npm warn cleanup     'D:\\项目\\code-switch-R\\frontend\\node_modules\\@rollup\\rollup-linux-s390x-gnu',
npm warn cleanup     [Error: EBUSY: resource busy or locked, rmdir 'D:\项目\code-switch-R\frontend\node_modules\@rollup\rollup-linux-s390x-gnu'] {
npm warn cleanup       errno: -4082,
npm warn cleanup       code: 'EBUSY',
npm warn cleanup       syscall: 'rmdir',
npm warn cleanup       path: 'D:\\项目\\code-switch-R\\frontend\\node_modules\\@rollup\\rollup-linux-s390x-gnu'
npm warn cleanup     }
npm warn cleanup   ]
npm warn cleanup ]
task: [generate:bindings (BUILD_FLAGS=-tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui")] wails3 generate bindings -f '-tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"' -clean=true -ts
[101m[97m Wails (v3.0.0-alpha.61) [0m[101m[0m[102m[97m Generate Bindings [0m[102m[0m
[102m[0m
up to date in 587ms

17 packages are looking for funding
  run `npm fund` for details
[30;43m[30;43m WARNING [0m[0m [33m[33m[warn] package codeswitch/services: function types are not supported by encoding/json[0m[0m
[30;46m[30;46m INFO [0m[0m [96m[96mProcessed: 425 Packages, 26 Services, 186 Methods, 3 Enums, 66 Models, 0 Events in 2.616836s.[0m[0m
[30;46m[30;46m INFO [0m[0m [96m[96mOutput directory: D:\项目\code-switch-R\frontend\bindings[0m[0m
[30;43m[30;43m WARNING [0m[0m [33m[33m1 warning emitted[0m[0m
task: [build:frontend (PRODUCTION=true)] npm run build -q

> frontend@1.0.0 build
> vue-tsc && vite build --mode production

[36mvite v7.2.1 [32mbuilding client environment for production...[36m[39m
transforming...
[32m✓[39m 977 modules transformed.
rendering chunks...
computing gzip size...
[2mdist/[22m[32mindex.html                 [39m[1m[2m    0.52 kB[22m[1m[22m[2m │ gzip:   0.32 kB[22m
[2mdist/[22m[35massets/index-Blmelmcf.css  [39m[1m[2m  135.29 kB[22m[1m[22m[2m │ gzip:  23.46 kB[22m
[2mdist/[22m[36massets/index-I13LJER4.js   [39m[1m[33m3,184.19 kB[39m[22m[2m │ gzip: 993.25 kB[22m
[33m
(!) Some chunks are larger than 500 kB after minification. Consider:
- Using dynamic import() to code-split the application
- Use build.rollupOptions.output.manualChunks to improve chunking: https://rollupjs.org/configuration-options/#output-manualchunks
- Adjust chunk size limit for this warning via build.chunkSizeWarningLimit.[39m
[32m✓ built in 3.09s[39m
task: [windows:generate:syso] wails3 generate syso -arch amd64 -icon windows/icon.ico -manifest windows/wails.exe.manifest -info windows/info.json -out ../wails_windows_amd64.syso
task: [windows:build] go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui" -o bin/CodeSwitch.exe
task: [windows:build] powershell Remove-item *.syso
```
