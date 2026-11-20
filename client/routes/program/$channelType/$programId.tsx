import { createFileRoute } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { $api } from "../../../lib/api/client.ts";
import type { components } from "../../../lib/api/schema.d.ts";
import { serviceOfProgram } from "../../../lib/service.ts";
import { useProgramQueries } from "../../../hooks/use-program-queries.ts";
import { fullProgramOf } from "../../../lib/api/programs.ts";
import { useNow } from "../../../hooks/use-now.ts";
import { formatYmdHms, zonedFromEpochMs } from "../../../lib/datetime.ts";
import ProgramModalDetail from "../../../components/organisms/Program/Modal/Detail.tsx";

type Program = components["schemas"]["MirakurunProgram"];

export const Route = createFileRoute("/program/$channelType/$programId")({
  component: ProgramDetailModal,
});

/** 番組詳細モーダル。URL の programId から対象番組を引いて表示する。 */
function ProgramDetailModal() {
  const { channelType, programId } = Route.useParams();
  const navigate = Route.useNavigate();
  const queryClient = useQueryClient();

  // 放送状態 (未開始/放送中/終了) を分単位で追従させる。
  const currentDate = zonedFromEpochMs(useNow(60_000));

  const { services, recordingSchedules } = useProgramQueries();
  const addRecordingSchedule = $api.useMutation("post", "/recording/schedules");
  const removeRecordingSchedule = $api.useMutation("delete", "/recording/schedules/{program_id}");

  // 詳細モーダルは description / extended を表示するため、一覧ではなく ID 指定で
  // フル番組を取得する。取得中はシェルだけ開いて待ち、404 等で取れなければ閉じる。
  // 非数の programId ではクエリを出さない (isPending が true のままになるため
  // open の条件からも除外し、モーダルは開かない)。
  const isNumericId = /^\d+$/.test(programId);
  const programQuery = $api.useQuery(
    "get",
    "/programs/{id}",
    { params: { path: { id: Number(programId) } } },
    { enabled: isNumericId },
  );

  const invalidateSchedules = () =>
    queryClient.invalidateQueries({
      queryKey: ["get", "/recording/schedules"],
    });

  const program = fullProgramOf(programQuery.data);
  const service = program ? serviceOfProgram(services.data ?? [], program) : undefined;
  const schedule = program
    ? (recordingSchedules.data ?? []).find((s) => s.program.id === program.id)
    : undefined;

  // 閉じる = 番組表 (レイアウト) へ戻る。?d= は保つ。
  const handleClose = () => {
    navigate({
      to: "/program/$channelType",
      params: { channelType },
      search: (prev) => prev,
    });
  };

  const handleAddRecordingSchedule = async (target: Program) => {
    await addRecordingSchedule.mutateAsync({
      body: {
        options: {
          contentPath: `${formatYmdHms(target.startAt)}_${target.id}_${target.name}.m2ts`,
        },
        programId: target.id,
      },
    });
    await invalidateSchedules();
  };

  const handleRemoveRecordingSchedule = async (target: Program) => {
    await removeRecordingSchedule.mutateAsync({
      params: { path: { program_id: target.id } },
    });
    await invalidateSchedules();
  };

  return (
    <ProgramModalDetail
      program={program}
      service={service}
      recordingSchedule={schedule}
      addRecordingSchedule={handleAddRecordingSchedule}
      removeRecordingSchedule={handleRemoveRecordingSchedule}
      loading={
        programQuery.isPending ||
        recordingSchedules.isPending ||
        addRecordingSchedule.isPending ||
        removeRecordingSchedule.isPending
      }
      currentDate={currentDate}
      open={isNumericId && (programQuery.isPending || program !== undefined)}
      onClose={handleClose}
    />
  );
}
