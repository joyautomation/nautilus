// Show.cs: show a program block in TIA Portal's own editor
// (siemens-ide.md section 3.4, "show").
//
// Only an agent started with TIAD_UI=1 has a TIA window to show it in;
// any other refuses, before TIA is asked anything. Showing reads the
// project and changes nothing in it.

using System;
using System.Collections.Generic;

namespace Tiad
{
    public sealed partial class Agent
    {
        // ShowNeedsUi is a show's refusal on an agent without TIA's window.
        public const string ShowNeedsUi = "this agent runs TIA Portal without its user interface, so there is no editor to show a block in: start the agent with TIAD_UI=1 (install-tiad.ps1 -Ui) to show one; such an agent refuses every download";

        void RegisterShow(Router r)
        {
            r.Add("POST", "/v1/sessions/{id}/show", Show);
        }

        object Show(Call c)
        {
            NeedTia();
            if (!cfg.Ui) throw TiadException.BadRequest("{0}", ShowNeedsUi);
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                string name = r.Str("block");
                r.Done();
                if (name == "") throw TiadException.BadRequest("block: a program block's name is required");
                ProjectBlock found = null;
                List<ProjectBlock> blocks = OnSession(s, delegate(ITia t) { return t.Blocks(s.Handle); });
                foreach (ProjectBlock b in blocks)
                {
                    // TIA's block names are unique in a PLC, compared
                    // without case.
                    if (string.Equals(b.Name, name, StringComparison.OrdinalIgnoreCase))
                    {
                        found = b;
                        break;
                    }
                }
                if (found == null)
                {
                    throw new TiadException(404, Kinds.NotFound, "no program block " + Json.Quote(name) + " in " + s.Project);
                }
                OnSession(s, delegate(ITia t)
                {
                    t.Show(s.Handle, found.Group, found.Name);
                    return true;
                });
                Log.Line("show session={0} project={1} block={2}", s.Id, Json.Quote(s.Project), Json.Quote(found.Name));
                var o = new Obj();
                o["block"] = found.Name;
                o["type"] = found.Type;
                o["number"] = found.Number;
                o["group"] = found.Group;
                return o;
            });
        }
    }
}
