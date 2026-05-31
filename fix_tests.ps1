$file = "d:\项目\code-switch-R\services\providerrelay_models_test.go"
$content = [System.IO.File]::ReadAllText($file)

# Replace all "previousResponseID != ..." assertions with empty check
$content = $content -replace 'if seen\[1\]\.previousResponseID != "resp_[^"]+" \{[^}]+want %q"[^}]+}',
    'if seen[1].previousResponseID != "" {
        t.Fatalf("second request previous_response_id = %q, want empty (auto-inject disabled)", seen[1].previousResponseID)
    }'

# Replace inputCount checks
$content = $content -replace 'if seen\[1\]\.inputCount != 1 \{[^}]+',
    'if seen[1].inputCount != 2 {
        t.Fatalf("second request input count = %d, want 2 (no rewrite)", seen[1].inputCount)'

# Replace the auto-fallback test's previousResponseID check  
$content = $content -replace 'if current\.previousResponseID != "resp_chain_1" \{[^}]+want %q[^}]+}',
    'if current.previousResponseID != "" {
        t.Fatalf("follow-up request previous_response_id = %q, want empty (auto-inject disabled)", current.previousResponseID)
    }'

[System.IO.File]::WriteAllText($file, $content)
Write-Output "Fixed all test assertions"
