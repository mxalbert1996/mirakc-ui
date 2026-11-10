import { describe, expect, it } from "vitest";
import { serviceNumber } from "./service.ts";
import type { components } from "./api/schema.d.ts";

type Service = components["schemas"]["MirakurunService"];
type ChannelType = components["schemas"]["ChannelType"];

function serviceWith(
  type: ChannelType,
  serviceId: number,
  remoteControlKeyId?: number,
): Service {
  return {
    id: serviceId,
    name: "テスト",
    channel: { type, channel: "1" },
    type: 1,
    networkId: 1,
    serviceId,
    remoteControlKeyId,
    hasLogoData: false,
  };
}

describe("serviceNumber", () => {
  it("GR はリモコン番号を返す", () => {
    expect(serviceNumber(serviceWith("GR", 1024, 1))).toBe(1);
  });

  it("BS / CS は mirakc が返す 0 を未設定として serviceId を返す", () => {
    expect(serviceNumber(serviceWith("BS", 151, 0))).toBe(151);
    expect(serviceNumber(serviceWith("CS", 296, 0))).toBe(296);
  });

  it("リモコン番号が無いサービスも serviceId を返す", () => {
    expect(serviceNumber(serviceWith("BS", 101))).toBe(101);
  });
});
