# How Agent Radar works

This page gives more information about the map and the data of Agent Radar.
For the installation and the flags, refer to the [README](../README.md).

## The map

The map has three columns:

1. **Surface.** The product that started the session, for example the GitHub Copilot app or the Copilot CLI.
2. **Sessions and child activity.** The sessions, the child sessions, the sub-agents, and the background shells.
3. **Data destinations.** The models, the local destinations, and the tools and services.

To see all the details of an item, select its marker. The side panel then shows the details.
If the window is narrow, you can scroll the map. The map does not make the labels smaller.

### Sub-agents

- A sub-agent has a diamond marker.
- A sub-agent shows below its parent session, in a group with a border.
- Below the name, the map shows the status and the current task of the sub-agent.
- A background sub-agent can continue after it completes its first task. The map shows its next turns and tools with the same name.
- To see all the completed sub-agents of the sessions, select **show idle / completed**.
  If you do not select it, the map shows only the sub-agents that completed recently.

### Child sessions

- A child session has a circle marker.
- A child session shows below the session that created it. A solid line connects the two sessions.
  The line stays also when the child session was created a long time ago.
- If a child session works and its parent session is idle, the map shows the parent with the label "Idle parent context".

### Messages between sessions

- When the radar finds a new message between two sessions, a dot moves one time along the line between the sessions.
- The map does not draw a new line for a message. It does not show old messages again.
- If the operating system is set to reduce motion, the receiver shows a short indication that does not move.
- The feed shows the details of the message.

### Background shells

- A detached background shell has a square marker below the session that owns it.
- The header shows the number of background shells separately.
- A background shell stays on the map when its agent is idle or disconnected.
- The radar does not count a background shell as a working agent or as a sub-agent.
- A session that has only background shells keeps the `idle` status.

## Data destinations

- The group "Local machine" contains only destinations that keep the data on this computer.
- The group "Tools & services" can contain local tools and remote services.
  Shell, build, and container commands are in this group, because they can send data to remote systems.
- The name of an MCP destination is the name of the MCP server.
  If the radar cannot find the server name, the destination is "MCP: unknown server".

## Model tokens for VS Code

VS Code does not record model calls. For VS Code chat sessions, the token panel shows "–" for calls.
If the panel adds known calls to the VS Code usage, the number has a "+" after it.

## How the radar finds background shells

The radar reads the files `session-state/*/detached-shells/*.json` in the Copilot home folder.

For each file, the radar makes sure that the shell still runs:

- The radar examines the supervisor process of the shell. It does not use the heartbeat of the owner session.
- On Linux, the radar also compares the boot ID and the start time of the process.
  Thus, the radar ignores old files and process IDs that the system gave to a different process.

The radar shows only the detached shells that have such a file.
A usual asynchronous shell does not have a file. Thus, the radar cannot find it after its tool call is complete.

The JSON summary contains the ID, the process ID, and the start time of each background shell.
It does not contain the command line or the log of the shell.
