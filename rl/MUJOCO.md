# MuJoCo for robot modeling and reinforcement learning

This guide shows how to turn a real robot into a MuJoCo model, wrap that
model as an RL environment, train a policy, and measure whether it can
transfer back to hardware. The example is a small two-joint arm, but the
workflow applies to manipulators, mobile robots and legged systems.

The central rule is simple: **model the robot's closed-loop behavior at
the same command boundary the policy will use on the real robot.** A
beautiful rendering is not a dynamics model, and a high simulator reward
does not prove that a policy will transfer.

For RL vocabulary, start with [RL basics](BASICS.md). For broader policy,
safety and deployment design, see [deep RL for physical robots](ROBOTICS.md).

## 1. What MuJoCo contributes

MuJoCo simulates articulated rigid bodies, joints, actuation, sensors and
contacts. A model is compiled from MJCF (MuJoCo's XML format) or an
importable URDF into an `MjModel`; an `MjData` holds one simulation's
state and workspace. The main simulation call advances that state by one
physics timestep.

An RL system adds the task around the physics:

```text
MJCF / URDF → MjModel + MjData → task environment → RL algorithm
                   ↑                    │                 │
                   └── reset, observe, step, reward ← action
```

MuJoCo answers “what happens when this command is applied to this modeled
robot?” The environment answers “what is the task, what does the policy
observe, and what counts as success?” The learning library (for example,
Gymnasium with a PyTorch implementation) optimizes the policy.

Use MuJoCo when contact-rich rigid-body dynamics, fast repeated rollouts,
and a Python-accessible simulator suit the task. It is not automatically a
good model of a particular robot: actuator electronics, low-level firmware,
backlash, cable forces, flexible links and perception pipelines may need
explicit models or measured approximations.

## 2. Gather the real robot's model data first

Do not start by tuning PPO. First collect the quantities that define the
robot and its command interface. Prefer the manufacturer's CAD, URDF and
controller documentation, then verify them against measurements on the
actual hardware.

| Data | Where it enters the simulation | How to verify it |
|---|---|---|
| Link geometry and joint transforms | Body tree, joint origins and axes | Compare FK at several measured joint configurations |
| Link mass, center of mass and inertia | Rigid-body dynamics | CAD mass properties, scale measurements, or system identification |
| Joint type, axis, sign, zero and limits | Kinematics and legal configuration | Move one joint at a time and compare encoder signs/limits |
| Motor, gearbox and transmission | Force/torque at each joint | Datasheet plus measured torque-speed or current response |
| Low-level controller | Mapping from command to motion | Test the same position/velocity/torque command used by the policy |
| Command rate, delay and filtering | Timing and actuator dynamics | Timestamped command/encoder logs |
| Sensors and frames | Policy observations | Check units, calibration, update rates, noise and delay |
| Contact surfaces and payload | Grasping, pushing and locomotion | Measure slip, settling and contact outcomes under known loads |

Keep one consistent unit system: MuJoCo does not enforce SI units. For
robot work, metres, kilograms, seconds, radians, newtons and newton-metres
make a good convention. Convert degrees, millimetres and RPM at the input
boundary, not in scattered parts of the policy.

### Start from an existing robot description

If the manufacturer supplies a URDF, load it as a baseline and check that
the model compiles. MuJoCo's XML loading supports URDF import, but do not
assume a URDF fully describes the simulation you need. Inspect and add the
actuators, sensors, contact properties, controller dynamics, delays and
task objects that are absent or represented differently in the source.
For an iterative simulation project, convert and maintain a versioned
MJCF model so those choices are explicit.

If you start from CAD, export each moving link with its origin and mass
properties. Simplify the collision geometry separately from the visual
meshes. Preserve a record of the CAD revision, scale, frame convention and
any simplifications so the model can be reproduced.

Treat the robot description as an engineering artifact: version it with
the environment, record the robot firmware/controller version, and avoid
untracked edits to a vendor file.

## 3. Build the MJCF model in layers

An MJCF robot is a tree of nested bodies. The parent-child transforms
describe kinematics; joints add degrees of freedom. Geoms provide visual
and collision shapes, sites name useful frames, actuators apply forces or
commands, and sensors expose simulated measurements.

Build and test in this order:

1. World, fixed base, body tree, joint origins and axes.
2. Joint ranges, signs, zero positions and a home keyframe.
3. Mass, center of mass and inertia for every moving link.
4. Collision shapes and contact properties.
5. Actuators that match the real policy command interface.
6. Sensors and task objects.
7. Timestep, solver and task-specific options.

The following MJCF is a **runnable structural example**, not a calibrated
model of a particular arm. Its dimensions, inertias, limits, damping and
torque limits are illustrative: replace them with measured values before
using it to make claims about hardware.

```xml
<mujoco model="two_link_arm">
  <compiler angle="radian" autolimits="true"/>
  <option timestep="0.002" gravity="0 0 -9.81" integrator="implicitfast"/>

  <default>
    <joint damping="0.1" armature="0.01" limited="true"/>
    <geom friction="1.0 0.005 0.0001" condim="3"/>
  </default>

  <worldbody>
    <geom name="floor" type="plane" size="2 2 0.1"/>
    <body name="base" pos="0 0 0.1">
      <geom name="base_collision" type="cylinder" size="0.06 0.05"/>

      <body name="upper_arm" pos="0 0 0.05">
        <joint name="shoulder" type="hinge" axis="0 1 0"
               range="-2.5 2.5"/>
        <inertial pos="0 0 0.15" mass="1.0"
                  diaginertia="0.0075 0.0075 0.001"/>
        <geom name="upper_collision" type="capsule"
              fromto="0 0 0 0 0 0.3" size="0.03"/>

        <body name="forearm" pos="0 0 0.3">
          <joint name="elbow" type="hinge" axis="0 1 0"
                 range="-2.6 2.6"/>
          <inertial pos="0 0 0.125" mass="0.6"
                    diaginertia="0.0032 0.0032 0.0001"/>
          <geom name="forearm_collision" type="capsule"
                fromto="0 0 0 0 0 0.25" size="0.025"/>
          <site name="tool" pos="0 0 0.25" size="0.01"/>
        </body>
      </body>
    </body>
  </worldbody>

  <actuator>
    <motor name="shoulder_torque" joint="shoulder"
           ctrllimited="true" ctrlrange="-4 4"/>
    <motor name="elbow_torque" joint="elbow"
           ctrllimited="true" ctrlrange="-2 2"/>
  </actuator>

  <sensor>
    <jointpos name="shoulder_position" joint="shoulder"/>
    <jointvel name="shoulder_velocity" joint="shoulder"/>
    <jointpos name="elbow_position" joint="elbow"/>
    <jointvel name="elbow_velocity" joint="elbow"/>
    <framepos name="tool_position" objtype="site" objname="tool"/>
  </sensor>
</mujoco>
```

The base is fixed because it has no joint. A mobile base or free-floating
robot needs the appropriate root degrees of freedom and ground contacts.
Hinge axes are expressed in the joint's local frame. A wrong origin or
axis can make the rendered robot look plausible while its kinematics are
wrong, so verify measured poses numerically as well as visually.

### Visual meshes are not collision models

Use detailed meshes for appearance if needed, but use simple, stable
collision shapes—capsules, boxes, cylinders or a deliberately simplified
mesh—for physics. A decorative shell can otherwise create accidental
contacts, slow collision detection or trap the solver in tiny features.
Separate visual and collision geoms where appropriate, and inspect
contacts in the viewer. For example, visual-only geoms can use
`contype="0"` and `conaffinity="0"`; do not disable collision on a geom
that must make contact with the task.

### Mass and inertia matter more than appearance

For each moving body, the mass, center of mass and inertia tensor determine
how it accelerates and how much torque is needed. Prefer inertial data
from the robot's CAD or manufacturer. Check that masses are positive,
inertia is physically plausible, centers of mass are in the correct local
frames, and units are consistent. MuJoCo can infer inertias from geoms,
but inferred values are only as good as the geometry and density choices;
do not rely on a visual mesh's appearance to supply accurate mass
properties.

If a real arm has a payload, represent its mass and center of mass at the
tool. If payload changes are expected, train and evaluate across a measured
payload range rather than silently using an empty gripper model.

## 4. Match what the policy commands on the hardware

An actuator is not just a force limit. The complete command path is:

```text
policy output → scaling/clamping → communication delay → robot controller
              → motor/gearbox → joint motion → measured observation
```

The simulator must approximate this whole path at the policy boundary.
If the deployed policy sends joint-position targets to a firmware servo,
training a policy that writes ideal joint torques directly creates a
different control problem. Model the position servo's tracking behavior,
rate limits, saturation and update delay—or retain that tested controller
in the loop and let the policy output bounded position targets.

Common action choices, from a higher-level to a lower-level interface:

| Policy action | Typical use | What the simulator must include |
|---|---|---|
| Discrete mode or goal | Select grasp, gait or waypoint | The lower-level controller and its response to each command |
| Position target or joint delta | Servo-controlled arms and grippers | Position-loop gains/response, target limits, rate limit and delay |
| Velocity target | Mobile bases and velocity-controlled joints | Velocity-loop response, acceleration limits and command timeout |
| Torque/current target | Research or torque-controlled hardware | Transmission, torque/current limits, motor dynamics, gravity and low-level timing |

Normalize continuous policy actions to `[-1, 1]`, then map them to explicit
physical bounds at one well-defined boundary. Keep the same units, order,
clipping and sign convention in simulation and deployment. Enforce
hardware limits independently of the learned policy; a reward penalty is
not a safety limiter.

MuJoCo motor actuators can model joint force/torque commands. Position and
velocity actuator shortcuts can model simple feedback behavior, but they
are not automatically identical to a particular robot's firmware. When
the real command passes through a PID loop, test a matching controller
model against command/encoder logs. Add measured actuator lag, filtering,
deadband, backlash or saturation only where the data supports it.

## 5. Pick physics and policy rates deliberately

MuJoCo's `timestep` is the physics integration interval. The policy may
act less frequently; holding one action over several physics steps is
action repeat (also called control decimation):

```text
physics timestep = 0.002 s
action repeat    = 10 physics steps
policy timestep  = 0.020 s  (50 Hz)
```

These numbers are examples, not universal settings. Select the policy
rate to match the real command interface and the task's dynamics. Select
the physics timestep small enough to resolve the fastest modeled motion
and contact behavior, then check stability and convergence. Reducing a
timestep increases simulation cost; increasing it too far can change
contacts or destabilize stiff control. A policy trained at one action
repeat and deployed at another sees a different system.

Log both simulated time and wall-clock throughput. Physics does not have
to run in real time for RL training; vectorized environments can run much
faster or slower than the robot. Real-time pacing matters when connecting
to viewers, hardware-in-the-loop or external sensors.

## 6. Calibrate the model against the robot

Calibration is a sequence of comparisons, not one visual inspection. Use
low-risk motions, synchronized timestamps and a known robot/controller
configuration. Fit a small number of parameters at a time so errors are
diagnosable.

### A. Compile, render and check kinematics

- Compile the MJCF and resolve all warnings before training.
- Check every joint name, order, sign, zero, unit and limit against the
  hardware interface.
- Compare forward-kinematics predictions with measured end-effector poses
  across the workspace, not only at the home pose.
- Check self-collision and workspace collision geometry at extreme but
  legal configurations.
- Confirm payload and tool frames match the deployed robot's frame tree.

### B. Match static and free-motion behavior

Move one joint at a time with small, bounded commands. Compare simulated
and measured position, velocity and settling response. For an arm, hold
poses under gravity and compare the effort/current required. This exposes
wrong mass properties, joint axes, damping, friction and controller
semantics before contact or RL makes the diagnosis harder.

### C. Identify controller and actuator dynamics

Record command, encoder state and timestamps while the existing controller
tracks safe step or trajectory commands. Estimate:

- command and sensor latency, including transport and filtering;
- closed-loop rise/settling time and overshoot;
- velocity/acceleration limits and saturation;
- deadband, friction, backlash or direction asymmetry when observable;
- differences between commanded and measured effort, if torque/current is
  exposed.

Fit the simulator to the observed **closed-loop** response if that is what
the policy will command. Do not identify a motor's theoretical torque
constant and assume that it describes a proprietary position servo.

### D. Validate contacts separately

For pushing, grasping and locomotion, test contact with simple physical
experiments: drop/settle a known object, slide a known load, lift a known
payload or compare foot slip on the intended surface. Contact friction,
compliance and collision geometry can dominate the task. MuJoCo's contact
solver is a numerical approximation; matching one coefficient does not
make every impact or soft material physically exact.

### E. Compare trajectories quantitatively

Replay the same reference commands in simulation and hardware and compare
joint-position error, end-effector error, settling time, contact outcome
and latency. Plot residuals over time and by configuration. If the model
fits at low speed but diverges at high speed, investigate actuator
saturation, delay and inertia before changing the RL reward.

| Symptom | Likely model/interface error to investigate |
|---|---|
| Wrong direction or mirrored reach | Joint sign, axis, frame or unit conversion |
| Correct endpoints, wrong path timing | Controller gains, action rate, delay or acceleration limit |
| Arm sags or oscillates under load | Mass/CoM/inertia, gravity, controller response or payload |
| Object slips only in reality | Contact geometry, friction, gripper force or material variation |
| Sim succeeds but hardware clips commands | Action scaling or missing saturation/rate limits |
| Policy sees values unlike training | Sensor order, frame, normalization, timestamp or noise model |

## 7. Wrap the model as an RL environment

An environment should expose the same task contract in training and
evaluation:

```text
reset(seed) → observation, info
step(action) → observation, reward, terminated, truncated, info
```

The observation should contain measurements available at deployment, in
the same frame, order, unit, rate and normalization. A state-based first
experiment might use joint positions and velocities plus the target pose;
add camera pixels only if perception is part of the problem you intend to
learn. Simulated ground-truth state that cannot be measured on the robot
is an information leak.

The action should match the real command layer described above. A
state-based reaching policy might output a bounded joint-position delta
that is held for a fixed number of physics steps. Convert that normalized
action using one shared, tested scaling function.

A task reward can combine progress, success and effort, but test the
behavior it induces. For example:

```text
reward = progress_weight × (distance_before − distance_after)
       + success_bonus × success
       − effort_weight × squared_action
       − collision_cost × unsafe_contact
```

Use an independent success test (for example, measured tool/object pose
and a stable grasp condition), not just the same shaped reward the policy
is optimizing. Set `terminated` for task completion or physical failure;
set `truncated` for an external time limit. They have different meanings
for value bootstrapping.

At reset, place the robot, object and goal in a known or deliberately
sampled state; clear velocities and controller state; then recompute
derived simulation quantities. A reset should not teleport through
collisions or leave stale actuator history. Test many reset seeds before
training so the initial-state distribution is intentional.

The low-level MuJoCo loop is small; the task environment owns action
scaling, action repeat, observations and rewards:

```python
import mujoco

model = mujoco.MjModel.from_xml_path("arm_scene.xml")
data = mujoco.MjData(model)
frame_skip = 10
torque_limits = [4.0, 2.0]  # illustrative only; use calibrated limits

def advance(normalized_action):
    # The action is held while the physics advances. The environment must
    # clip it to [-1, 1] and use the same mapping as deployment.
    for _ in range(frame_skip):
        data.ctrl[:] = [
            max(-1.0, min(1.0, float(a))) * limit
            for a, limit in zip(normalized_action, torque_limits)
        ]
        mujoco.mj_step(model, data)
    return observe_robot_and_task(model, data)
```

`observe_robot_and_task` is task-specific: implement it from named joints,
sensors and sites in your model. If the real policy sends position targets,
replace the torque action model with the matching simulated controller;
do not copy the torque example unchanged. A complete Gymnasium environment
also implements `reset`, reward, success/failure detection, time-limit
truncation and `info` metrics. Check its reset and step contract with
Gymnasium's environment checker before launching a long training job.

The official Python package exposes MuJoCo's native API:

```bash
python -m pip install mujoco gymnasium
```

Pin the Python, MuJoCo, Gymnasium and RL-library versions for each
experiment. Keep training configuration, model XML, random seeds and
policy checkpoints together.

## 8. Train a policy without hiding environment mistakes

Start with a scripted controller or a random-policy baseline and verify
that the observation and action spaces are correct. Then compare a small
number of plausible algorithms under the same task and evaluation setup:

| Situation | Useful first comparison |
|---|---|
| Continuous actions, cheap parallel simulation | PPO versus SAC |
| Replay/data reuse is important | SAC or TD3 |
| Discrete high-level mode selection | DQN or PPO |
| Demonstrations are available | Behaviour cloning before RL fine-tuning |
| Hardware interaction is scarce | Offline learning or simulation-first training, with conservative evaluation |

PPO is often a straightforward on-policy baseline when simulation can
produce many rollouts. SAC reuses transitions through replay and is often
useful for continuous control. Neither choice repairs incorrect units,
bad resets, poor reward design or a mismatched controller.

Use multiple independent training seeds and report held-out evaluation
episodes. Normalize observations using training statistics, freeze those
statistics for evaluation and deployment, and save them alongside the
policy. Evaluate without exploration noise unless stochastic behavior is
an intentional part of the product. Track success rate, return, task time,
constraint violations, contact failures and intervention counts—not just
the training reward curve.

Parallel simulation can improve sample throughput. Start with a modest
number of environments and profile the full training loop: physics,
rendering, observation construction and policy updates. Headless physics
usually avoids rendering overhead. MuJoCo's CPU engine is appropriate for
many tasks; MJX or other accelerated backends can help with large batches,
but validate contact and task behavior against the backend you plan to
evaluate because different numerical paths can produce different
trajectories.

## 9. Close the sim-to-real gap methodically

Domain randomization trains against a distribution of plausible systems.
It should cover **measured uncertainty**, not arbitrary extremes. A useful
sequence is:

1. Calibrate a nominal model against the physical robot.
2. Measure which parameters vary or remain uncertain.
3. Randomize those parameters during training, within physically plausible
   ranges.
4. Evaluate on fixed, held-out parameter combinations, including the
   nominal model and boundary cases.
5. Compare real failures with simulation and update the model or
   uncertainty range using evidence.

Parameters often randomized include link mass/center of mass, payload,
joint damping/friction, actuator strength and time constant, command
latency, observation noise, object pose, surface friction and external
disturbances. Vary coupled quantities consistently: changing a link's
mass without changing its inertia can create an implausible body. Do not
randomize hard robot limits to train a policy to violate the limits; keep
constraints fixed and enforced.

Visual randomization is only relevant when the policy consumes images.
Then camera extrinsics, exposure, lighting, textures, motion blur and
latency may matter. For state-based control, first make the state and
control model transferable; pixel complexity is not automatically an
improvement.

Use a curriculum: solve an easier task with clean observations and
conservative motions, then broaden initial conditions, payloads, noise and
disturbances as performance becomes reliable. If randomized training
causes the policy to fail in the nominal simulator, the distribution may
be too broad, inconsistent or too hard for the current reward/algorithm.

## 10. A complete arm-to-hardware workflow

For a reaching or pick-and-place task, an end-to-end project can proceed
as follows:

1. **Define the deployment boundary.** Choose exactly what the policy
   commands (for example, bounded Cartesian deltas sent to a tested
   position controller) and what state it receives.
2. **Assemble the robot description.** Start from the vendor URDF/CAD;
   establish joint order, units, frames, limits, mass properties, tool and
   payload. Record the source revisions.
3. **Build and inspect MJCF.** Add simplified collision geometry, contact
   surfaces, actuators, sensors and task objects. Compile it and inspect
   joint axes and contacts in the viewer.
4. **Validate the open-loop model.** Compare FK, static gravity response,
   safe trajectory tracking, command latency and settling against hardware
   logs. Correct systematic errors before adding RL.
5. **Create and test the environment.** Implement reproducible resets,
   deployment-available observations, action scaling, clear success
   criteria, termination/truncation and metrics.
6. **Establish baselines.** Measure random and scripted policies; if
   demonstrations exist, train behaviour cloning and evaluate it on held
   out object positions.
7. **Train in simulation.** Use PPO or SAC as initial continuous-control
   comparisons, multiple seeds, sensible reward scales and a staged
   curriculum. Keep a frozen validation set.
8. **Test robustness.** Evaluate nominal and randomized dynamics, new
   object poses, payload variation, sensor delay/noise and worst-case
   failures. Investigate failures by category rather than tuning to the
   best seed.
9. **Package the exact policy contract.** Bundle weights, observation
   ordering/units, normalizer, action scaling/bounds, controller
   configuration and compatible model/checkpoint identifiers.
10. **Transfer in stages.** Replay on recorded observations, run in
    simulation with hardware timing, then test under the robot's approved
    supervised procedures in a restricted workspace and envelope. Retain
    a known-good controller, independent limits, watchdog and emergency
    stop.
11. **Measure the real gap.** Compare success, trajectory error, latency,
    contacts, safety interventions and failure modes with simulation.
    Change the model or policy only against logged evidence; preserve the
    previous safe version for rollback.

For a grasping task, the environment might observe joint positions and
velocities, end-effector pose, gripper state and object pose; command a
bounded tool-frame delta and gripper target; reward verified stable lifts;
and terminate on success or an unsafe collision. Camera observations can
be added after the state-based control and perception interfaces have
been validated separately.

## 11. Common failure modes

- **The robot looks right, but motion is wrong.** Check joint origins,
  axes, signs, frame conventions and encoder zero before retuning the
  policy.
- **Training works only with direct torque.** The real hardware may expose
  position or velocity commands; model its controller and train through
  that same boundary.
- **The policy exploits reward.** Use independent success measurement,
  inspect trajectories and adversarially test reward shortcuts.
- **Randomization makes things worse.** First correct the nominal model;
  then randomize measured uncertainty with physically consistent samples.
- **Sim grasps, reality slips.** Compare collision shapes, contact points,
  friction, gripper force, payload and timing; test several surfaces and
  objects.
- **Sim success falls apart at deployment rate.** Match policy rate,
  action repeat, sensor rates, latency and command hold behavior.
- **A high mean hides dangerous failures.** Report worst-case categories,
  success confidence intervals, collisions and safety interventions.
- **A safety penalty is treated as a guarantee.** Enforce joint, speed,
  workspace and force limits in an independent controller/safety layer.

The policy is only one part of the transfer system. Model quality, data
alignment, controller matching, reset design, evaluation and safety
boundaries usually determine whether a simulator-trained behavior is
useful on a real robot.

## Further reading

- [MuJoCo documentation: modeling](https://mujoco.readthedocs.io/en/stable/modeling.html)
- [MuJoCo documentation: MJCF XML reference](https://mujoco.readthedocs.io/en/stable/XMLreference.html)
- [MuJoCo documentation: Python bindings](https://mujoco.readthedocs.io/en/stable/python.html)
- [MuJoCo documentation: simulation](https://mujoco.readthedocs.io/en/stable/programming/simulation.html)
- [MuJoCo Menagerie](https://github.com/google-deepmind/mujoco_menagerie) — curated MJCF robot models
- [MuJoCo Playground](https://playground.mujoco.org/) — robotics tasks and sim-to-real examples
- [Gymnasium Robotics](https://robotics.farama.org/) — continuous-control environments and APIs
- [RL basics](BASICS.md) and the [deep RL algorithm reference](README.md)
- [Physical robot RL and deployment](ROBOTICS.md)
