import { describe, expect, it } from "vitest";
import {
  buildChannelGroups,
  type ChannelGroup,
  serviceNumber,
  sortChannelGroups,
  sortServices,
} from "./service.ts";
import type { components } from "./api/schema.d.ts";

type Service = components["schemas"]["MirakurunService"];
type Channel = components["schemas"]["MirakurunChannel"];
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

describe("sortServices", () => {
  it("選局番号の昇順に並べる", () => {
    const sorted = sortServices([
      serviceWith("BS", 151, 0),
      serviceWith("GR", 1040, 4),
      serviceWith("BS", 101),
      serviceWith("GR", 1024, 1),
    ]);
    expect(sorted.map((s) => s.serviceId)).toEqual([1024, 1040, 101, 151]);
  });

  it("GR は serviceId よりリモコン番号を優先する", () => {
    const sorted = sortServices([
      serviceWith("GR", 1048, 6),
      serviceWith("GR", 1064, 5),
    ]);
    expect(sorted.map((s) => s.serviceId)).toEqual([1064, 1048]);
  });
});

describe("sortChannelGroups", () => {
  function group(id: string, services: Service[]): ChannelGroup {
    return { id, type: "GR", name: id, services };
  }

  it("配下の先頭サービスの選局番号順に並べる", () => {
    const sorted = sortChannelGroups([
      group("c", [serviceWith("GR", 1040, 4)]),
      group("a", [serviceWith("GR", 1024, 1)]),
      group("b", [serviceWith("GR", 1032, 2)]),
    ]);
    expect(sorted.map((g) => g.id)).toEqual(["a", "b", "c"]);
  });

  it("サービスを持たない group は末尾へ回す", () => {
    const sorted = sortChannelGroups([
      group("z", []),
      group("a", [serviceWith("GR", 1024, 1)]),
    ]);
    expect(sorted.map((g) => g.id)).toEqual(["a", "z"]);
  });
});

describe("buildChannelGroups", () => {
  function channelOf(
    channel: string,
    type: ChannelType,
    services: Service[],
  ): Channel {
    return {
      channel,
      name: channel,
      type,
      services: services.map((s) => ({
        id: s.id,
        name: s.name,
        networkId: s.networkId,
        serviceId: s.serviceId,
      })),
    };
  }

  it("チャンネルと配下サービスを選局順に並べる", () => {
    const groups = buildChannelGroups(
      [
        channelOf("24", "GR", [serviceWith("GR", 1048, 5)]),
        channelOf("multi", "GR", [
          serviceWith("GR", 1064, 8),
          serviceWith("GR", 1056, 6),
        ]),
        channelOf("27", "GR", [serviceWith("GR", 1024, 1)]),
      ],
      [
        serviceWith("GR", 1048, 5),
        serviceWith("GR", 1064, 8),
        serviceWith("GR", 1056, 6),
        serviceWith("GR", 1024, 1),
      ],
    );
    expect(groups.map((g) => g.id)).toEqual(["27", "24", "multi"]);
    expect(groups[2].services.map((s) => s.serviceId)).toEqual([1056, 1064]);
  });
});
