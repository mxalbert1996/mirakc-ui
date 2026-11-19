ntfy/Discord への録画イベント通知の設定 (保存 API の入力)。

`token` と `discordWebhookUrl` は書き込み専用の秘密情報で、応答には値を出さない。未指定または空文字なら保存済みの値を保持し、非空文字なら置き換え、明示的な `null` なら空にする。

各イベントのトグルが 1 つでも有効な場合は、通知先 (ntfy の `url` または Discord の `discordWebhookUrl`) の少なくとも一方が必須となる。
