"use client";

import { useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  applyEdgeChanges,
  applyNodeChanges,
  Background,
  BackgroundVariant,
  MiniMap,
  MarkerType,
  Panel,
  ReactFlow,
  useReactFlow,
  type Connection,
  type Edge,
  type EdgeChange,
  type NodeChange,
} from "@xyflow/react";
import { Maximize2, Minus, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { workflowNodeTypes } from "@/components/workflows/workflow-graph";
import { buildWorkflowGraph, type WorkflowNode } from "@/lib/workflow/graph";
import type { NormalizedWorkflowSpec } from "@/lib/workflow/normalize";
import type { WorkflowPositionMap } from "@/lib/workflow/editor";

type FlowState = {
  spec: NormalizedWorkflowSpec;
  layout: unknown;
  selectedTask: string | null;
  taskErrors: Readonly<Record<string, readonly string[]>>;
  nodes: WorkflowNode[];
  edges: Edge[];
};

function ZoomControls({ zoom }: { zoom: number }) {
  const flow = useReactFlow();
  return (
    <Panel position="bottom-left" className="m-2 flex items-center gap-1 border border-border bg-card/95 p-1 shadow-none">
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Zoom out" onClick={() => { void flow.zoomOut({ duration: 120 }); }}>
        <Minus aria-hidden="true" className="size-3" />
      </Button>
      <span className="min-w-10 text-center font-mono text-[9px] tabular-nums text-muted-foreground">{`${String(Math.round(zoom * 100))}%`}</span>
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Zoom in" onClick={() => { void flow.zoomIn({ duration: 120 }); }}>
        <Plus aria-hidden="true" className="size-3" />
      </Button>
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Fit canvas" onClick={() => { void flow.fitView({ duration: 150, padding: 0.16 }); }}>
        <Maximize2 aria-hidden="true" className="size-3" />
      </Button>
    </Panel>
  );
}

export function EditorCanvas({
  spec,
  layout,
  selectedTask,
  taskErrors,
  message,
  onSelectTask,
  onAddDependency,
  onRemoveDependency,
  onLayoutChange,
  onDeleteTask,
}: {
  spec: NormalizedWorkflowSpec;
  layout: unknown;
  selectedTask: string | null;
  taskErrors: Readonly<Record<string, readonly string[]>>;
  message: string;
  onSelectTask: (taskName: string | null) => void;
  onAddDependency: (target: string, dependency: string) => void;
  onRemoveDependency: (target: string, dependency: string) => void;
  onLayoutChange: (positions: WorkflowPositionMap) => void;
  onDeleteTask: (taskName: string) => void;
}) {
  const graph = useMemo(
    () => buildWorkflowGraph(spec, layout, {}, selectedTask, taskErrors),
    [layout, selectedTask, spec, taskErrors],
  );
  const [flowState, setFlowState] = useState<FlowState | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<Edge | null>(null);
  const [zoom, setZoom] = useState(1);
  const canvasRef = useRef<HTMLDivElement>(null);
  const currentFlow = flowState
    && flowState.spec === spec
    && flowState.layout === layout
    && flowState.selectedTask === selectedTask
    && flowState.taskErrors === taskErrors
    ? flowState
    : { spec, layout, selectedTask, taskErrors, nodes: graph.nodes, edges: graph.edges };

  function updateNodes(changes: NodeChange<WorkflowNode>[]) {
    setFlowState((previous) => {
      const state = previous
        && previous.spec === spec
        && previous.layout === layout
        && previous.selectedTask === selectedTask
        && previous.taskErrors === taskErrors
        ? previous
        : { spec, layout, selectedTask, taskErrors, nodes: graph.nodes, edges: graph.edges };
      return { ...state, nodes: applyNodeChanges(changes, state.nodes) };
    });
  }

  function updateEdges(changes: EdgeChange[]) {
    setFlowState((previous) => {
      const state = previous
        && previous.spec === spec
        && previous.layout === layout
        && previous.selectedTask === selectedTask
        && previous.taskErrors === taskErrors
        ? previous
        : { spec, layout, selectedTask, taskErrors, nodes: graph.nodes, edges: graph.edges };
      return { ...state, edges: applyEdgeChanges(changes, state.edges) };
    });
  }

  function handleConnect(connection: Connection) {
    if (connection.source && connection.target) onAddDependency(connection.target, connection.source);
  }

  function handleDeleteKey(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== "Delete" && event.key !== "Backspace") return;
    event.preventDefault();
    if (selectedEdge) {
      onRemoveDependency(selectedEdge.target, selectedEdge.source);
      setSelectedEdge(null);
    } else if (selectedTask) {
      onDeleteTask(selectedTask);
    }
  }

  if (spec.spec.tasks.length === 0) {
    return <div className="grid h-full min-h-72 place-items-center border border-border bg-card font-mono text-xs uppercase tracking-[0.08em] text-muted-foreground">Add a task from the toolbar</div>;
  }

  return (
    <div
      ref={canvasRef}
      tabIndex={0}
      className="workflow-canvas react-flow relative h-full min-h-[26rem] border border-border bg-card outline-none"
      onKeyDown={handleDeleteKey}
    >
      {message ? <div role="status" className="absolute left-1/2 top-3 z-20 -translate-x-1/2 border border-border bg-popover px-3 py-2 font-mono text-[10px] text-foreground shadow-none">{message}</div> : null}
      {selectedEdge ? (
        <div className="absolute right-3 top-3 z-20">
          <Button type="button" variant="outline" size="sm" onClick={() => {
            onRemoveDependency(selectedEdge.target, selectedEdge.source);
            setSelectedEdge(null);
          }}>
            Remove dependency ×
          </Button>
        </div>
      ) : null}
      <ReactFlow
        nodes={currentFlow.nodes}
        edges={currentFlow.edges.map((edge) => edge.id === selectedEdge?.id ? {
          ...edge,
          style: { ...edge.style, stroke: "var(--primary)", strokeWidth: 2 },
        } : edge)}
        nodeTypes={workflowNodeTypes}
        onNodesChange={updateNodes}
        onEdgesChange={updateEdges}
        onNodeClick={(_, node) => {
          setSelectedEdge(null);
          onSelectTask(node.id);
          canvasRef.current?.focus();
        }}
        onEdgeClick={(_, edge) => {
          onSelectTask(null);
          setSelectedEdge(edge);
          canvasRef.current?.focus();
        }}
        onPaneClick={() => {
          onSelectTask(null);
          setSelectedEdge(null);
        }}
        onConnect={handleConnect}
        onNodeDragStop={(_, _node, nodes) => {
          onLayoutChange(Object.fromEntries(nodes.map((node) => [node.id, node.position])));
        }}
        onMove={(_, viewport) => { setZoom(viewport.zoom); }}
        nodesDraggable
        nodesConnectable
        edgesReconnectable={false}
        elementsSelectable
        deleteKeyCode={null}
        fitView
        fitViewOptions={{ padding: 0.16 }}
        defaultEdgeOptions={{ markerEnd: { type: MarkerType.ArrowClosed, color: "var(--muted-foreground)" } }}
      >
        <Background variant={BackgroundVariant.Dots} gap={22} size={1} color="var(--border)" />
        <ZoomControls zoom={zoom} />
        <MiniMap
          pannable
          zoomable
          position="bottom-right"
          className="!m-2 !border !border-border !bg-card"
          maskColor="rgba(0,0,0,0.55)"
          nodeColor="var(--muted-foreground)"
          nodeStrokeColor="var(--primary)"
        />
      </ReactFlow>
    </div>
  );
}
