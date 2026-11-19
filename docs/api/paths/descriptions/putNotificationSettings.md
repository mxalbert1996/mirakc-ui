ntfy への通知設定を保存する。

`url` とイベントのトグルは入力で上書きする。`token` と `discordWebhookUrl` は未指定または空文字なら保存済みの値を保持し、非空文字なら置き換え、明示的な `null` なら空にする。イベントのトグルが 1 つでも有効な場合は、通知先 (ntfy の `url` または Discord の `discordWebhookUrl`) の少なくとも一方が必須となる。
