ストリーム視聴用のワンタイムチケットを発行する。

EventSource と mpegts.js はリクエストヘッダを設定できないため、これらが開くストリーム (`GET /comments/services/{id}/stream` と `GET /transcode/services/{id}`) では `ticket` クエリパラメータでも認証できる。自動再接続 (EventSource) と同一 URL の再要求 (mpegts.js) に対応するため、チケットは厳密な 1 回限りとせず、約 60 秒・最大 5 回の使用で失効する。発行にはヘッダによる認証が必要で、チケットで別のチケットは発行できない。
